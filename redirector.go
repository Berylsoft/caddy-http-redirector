// Package redirector implements a Caddy HTTP handler that redirects
// requests of the form /{scope}/{key} according to per-scope key/link
// tables. It is a port of the standalone Rust project
// https://github.com/stackinspector/http-redirector.
package redirector

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"go.uber.org/zap"
)

const (
	// moduleVersion is reported in the Init log event.
	moduleVersion = "0.1.0"

	// maxConfigSize limits how much data is read from a config source.
	maxConfigSize = 10 << 20 // 10 MiB

	// configFetchTimeout bounds remote config loading while provisioning.
	configFetchTimeout = 30 * time.Second
)

func init() {
	caddy.RegisterModule(Handler{})
}

// ScopeConfig declares one redirect scope and where its key/link table
// comes from. Source is either an http(s) URL or a local file path.
type ScopeConfig struct {
	Name   string `json:"name,omitempty"`
	Source string `json:"source,omitempty"`
}

// Handler is the HTTP middleware that serves the redirects. Requests are
// matched as /{scope}/{key}; a hit is answered with 307 (or with the mapped
// value as text when ReturnValue is set) and a miss with 404.
//
// Its state is loaded once at provision time. There is deliberately no
// request-triggered update mechanism: update the config and reload Caddy.
type Handler struct {
	// Scopes are the redirect scopes served by this handler. Each scope
	// has a name and a config source (URL or local path).
	Scopes []ScopeConfig `json:"scopes,omitempty"`

	// ReqIDHeader is the name of the request ID header to record in the
	// access log. If empty, no request ID is logged.
	ReqIDHeader string `json:"req_id_header,omitempty"`

	// ReturnValue makes the handler respond with the mapped value as
	// text/plain instead of a 307 redirect.
	ReturnValue bool `json:"return_value,omitempty"`

	// NoFillHTTPS disables automatically prefixing mapped values with
	// "https://" when they do not already start with a URL scheme.
	NoFillHTTPS bool `json:"no_fill_https,omitempty"`

	state  map[string]scopeState
	logger *zap.Logger
	client *http.Client
}

// scopeState is the provisioned, immutable state of one scope. Its JSON
// shape is part of the Init log event and must not be changed casually.
type scopeState struct {
	URL string            `json:"url"`
	Map map[string]string `json:"map"`
}

// CaddyModule returns the Caddy module information.
func (Handler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.redirector",
		New: func() caddy.Module { return new(Handler) },
	}
}

// Validate checks the handler configuration without any I/O.
func (h *Handler) Validate() error {
	if len(h.Scopes) == 0 {
		return errors.New("at least one scope is required")
	}
	seen := make(map[string]struct{}, len(h.Scopes))
	for i, scope := range h.Scopes {
		if scope.Name == "" {
			return fmt.Errorf("scope %d: name is required", i)
		}
		if strings.ContainsAny(scope.Name, "/\x00") {
			return fmt.Errorf("scope %q: name must not contain '/' or NUL", scope.Name)
		}
		if scope.Source == "" {
			return fmt.Errorf("scope %q: config source is required", scope.Name)
		}
		if _, ok := seen[scope.Name]; ok {
			return fmt.Errorf("duplicate scope %q", scope.Name)
		}
		seen[scope.Name] = struct{}{}
	}
	return nil
}

// Provision loads every scope's key/link table and emits the Init event.
func (h *Handler) Provision(ctx caddy.Context) error {
	return h.provision(ctx.Logger(), &http.Client{Timeout: configFetchTimeout})
}

// provision is the testable core of Provision. The logger and HTTP client
// are injected so tests do not need a fully loaded Caddy configuration.
func (h *Handler) provision(logger *zap.Logger, client *http.Client) error {
	if err := h.Validate(); err != nil {
		return err
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	h.logger = logger
	h.client = client

	state := make(map[string]scopeState, len(h.Scopes))
	for _, scope := range h.Scopes {
		content, err := h.loadConfig(scope.Source)
		if err != nil {
			return fmt.Errorf("scope %q: loading config %q: %w", scope.Name, scope.Source, err)
		}
		table, err := parseTable(content, h.NoFillHTTPS)
		if err != nil {
			return fmt.Errorf("scope %q: parsing config %q: %w", scope.Name, scope.Source, err)
		}
		state[scope.Name] = scopeState{URL: scope.Source, Map: table}
	}
	h.state = state

	// This mirrors the Rust implementation's Init event. Update-related
	// fields are kept (always null/false) so log consumers keep seeing the
	// same event shape even though updating is no longer supported.
	h.logger.Info(h.initEventLine())

	return nil
}

func (h *Handler) loadConfig(source string) (string, error) {
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		if h.client == nil {
			h.client = &http.Client{Timeout: configFetchTimeout}
		}
		resp, err := h.client.Get(source)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("HTTP status %s", resp.Status)
		}
		return readConfig(resp.Body)
	}

	f, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return readConfig(f)
}

// readConfig reads a key/link table with a size limit and validates UTF-8.
func readConfig(r io.Reader) (string, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxConfigSize+1))
	if err != nil {
		return "", err
	}
	if len(body) > maxConfigSize {
		return "", fmt.Errorf("config exceeds %d bytes", maxConfigSize)
	}
	if !utf8.Valid(body) {
		return "", errors.New("config is not valid UTF-8")
	}
	return string(body), nil
}

// parseTable parses a key/link table. Each non-empty line contains exactly
// one key and one target separated by whitespace.
func parseTable(content string, noFillHTTPS bool) (map[string]string, error) {
	table := make(map[string]string)
	for lineNo, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 2 {
			return nil, fmt.Errorf("line %d: expected 2 fields, got %d", lineNo+1, len(fields))
		}
		key, target := fields[0], fields[1]
		if _, exists := table[key]; exists {
			return nil, fmt.Errorf("line %d: duplicate key %q", lineNo+1, key)
		}
		if !noFillHTTPS &&
			!strings.HasPrefix(target, "http://") &&
			!strings.HasPrefix(target, "https://") {
			target = "https://" + target
		}
		table[key] = target
	}
	return table, nil
}

// ServeHTTP implements caddyhttp.MiddlewareHandler. It is terminal for
// every request that reaches it, just like the standalone server was.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request, _ caddyhttp.Handler) error {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return nil
	}

	scope, key, ok := splitScopeKey(r.URL.Path)
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return nil
	}

	xff, ok := nullStringHeader(r, "X-Forwarded-For")
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		return nil
	}
	ua, ok := nullStringHeader(r, "User-Agent")
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		return nil
	}
	var reqID *string
	if h.ReqIDHeader != "" {
		reqID, ok = nullStringHeader(r, h.ReqIDHeader)
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			return nil
		}
	}

	now := time.Now().UnixMilli()

	var target string
	var hit bool
	if scopeState, exists := h.state[scope]; exists {
		target, hit = scopeState.Map[key]
	}

	if h.logger != nil {
		h.logger.Info(h.requestEventLine(now, xff, reqID, ua, r.RemoteAddr, scope, key, hit))
	}

	if !hit {
		w.WriteHeader(http.StatusNotFound)
		return nil
	}

	if h.ReturnValue {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, target)
		return nil
	}

	w.Header().Set("Location", target)
	w.WriteHeader(http.StatusTemporaryRedirect)
	return nil
}

// splitScopeKey extracts the first two path segments after the leading
// slash. Extra segments and query strings are ignored, matching the Rust
// implementation.
func splitScopeKey(path string) (scope, key string, ok bool) {
	parts := strings.SplitN(path, "/", 4)
	if len(parts) < 3 {
		return "", "", false
	}
	return parts[1], parts[2], true
}

// nullStringHeader returns the first value of the named header. The bool is
// false if the value contains bytes that are not visible ASCII, matching the
// Rust implementation's HeaderValue::to_str() check and its 400 response.
func nullStringHeader(r *http.Request, name string) (*string, bool) {
	values := r.Header.Values(name)
	if len(values) == 0 {
		return nil, true
	}
	value := values[0]
	for i := 0; i < len(value); i++ {
		b := value[i]
		if b == '\t' || (b >= 0x20 && b <= 0x7e) {
			continue
		}
		return nil, false
	}
	return &value, true
}

// remoteFrom builds the "from" field: every X-Forwarded-For entry in order,
// followed by the address of the immediate peer. The peer address keeps its
// port, matching Rust's SocketAddr display format.
func remoteFrom(xff *string, remoteAddr string) []string {
	from := make([]string, 0, 3)
	if xff != nil {
		from = append(from, strings.Split(*xff, ",")...)
	}
	from = append(from, remoteAddr)
	return from
}

// Log event types. Field order mirrors the serde struct order of the Rust
// implementation, and fields are intentionally not omitempty.

type logEvent struct {
	Time  int64    `json:"time"`
	Event logInner `json:"event"`
}

type logInner struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

type initEventData struct {
	Ver         string                `json:"ver"`
	State       map[string]scopeState `json:"state"`
	ReqIDHeader *string               `json:"req_id_header"`
	UpdateKey   *string               `json:"update_key"`
	AllowUpdate bool                  `json:"allow_update"`
}

type requestEventData struct {
	From  []string `json:"from"`
	ReqID *string  `json:"req_id"`
	UA    *string  `json:"ua"`
	Scope string   `json:"scope"`
	Key   string   `json:"key"`
	Inner logInner `json:"inner"`
}

type getEventData struct {
	Hit bool `json:"hit"`
}

func (h *Handler) initEventLine() string {
	return h.initEventLineAt(time.Now().UnixMilli())
}

func (h *Handler) initEventLineAt(now int64) string {
	var reqIDHeader *string
	if h.ReqIDHeader != "" {
		reqIDHeader = &h.ReqIDHeader
	}
	return marshalEvent(logEvent{
		Time: now,
		Event: logInner{
			Type: "Init",
			Data: initEventData{
				Ver:         moduleVersion,
				State:       h.state,
				ReqIDHeader: reqIDHeader,
				UpdateKey:   nil,
				AllowUpdate: false,
			},
		},
	})
}

func (h *Handler) requestEventLine(now int64, xff, reqID, ua *string, remoteAddr, scope, key string, hit bool) string {
	return marshalEvent(logEvent{
		Time: now,
		Event: logInner{
			Type: "Request",
			Data: requestEventData{
				From:  remoteFrom(xff, remoteAddr),
				ReqID: reqID,
				UA:    ua,
				Scope: scope,
				Key:   key,
				Inner: logInner{Type: "Get", Data: getEventData{Hit: hit}},
			},
		},
	})
}

// marshalEvent encodes one log event as compact JSON without HTML escaping,
// matching serde_json's output. encoding/json's trailing newline is removed
// because the log writers add their own line endings.
func marshalEvent(event logEvent) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(event); err != nil {
		// The event types only contain strings, maps, slices, and bools, so
		// this cannot normally happen.
		return `{"time":0,"event":{"type":"Error","data":{"message":"failed to encode log event"}}}`
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

var (
	_ caddy.Module                = (*Handler)(nil)
	_ caddy.Provisioner           = (*Handler)(nil)
	_ caddy.Validator             = (*Handler)(nil)
	_ caddyhttp.MiddlewareHandler = (*Handler)(nil)
)
