package redirector

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestParseTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		content     string
		noFillHTTPS bool
		want        map[string]string
		wantErr     bool
	}{
		{
			name: "bare targets get https",
			content: `
home    example.com/

docs    docs.example.com/
`,
			want: map[string]string{
				"home": "https://example.com/",
				"docs": "https://docs.example.com/",
			},
		},
		{
			name:    "absolute targets are preserved",
			content: "a http://example.com/a\nb https://example.com/b\n",
			want: map[string]string{
				"a": "http://example.com/a",
				"b": "https://example.com/b",
			},
		},
		{
			name:        "no fill https",
			content:     "alice@example.com Alice\nbob@example.com Bob\n",
			noFillHTTPS: true,
			want: map[string]string{
				"alice@example.com": "Alice",
				"bob@example.com":   "Bob",
			},
		},
		{
			name:    "tabs and CRLF are whitespace",
			content: "a\texample.com/a\r\nb   example.com/b\r\n",
			want: map[string]string{
				"a": "https://example.com/a",
				"b": "https://example.com/b",
			},
		},
		{
			name:    "empty content",
			content: "",
			want:    map[string]string{},
		},
		{
			name:    "duplicate key",
			content: "a example.com/1\na example.com/2\n",
			wantErr: true,
		},
		{
			name:    "too many fields",
			content: "a example.com/1 extra\n",
			wantErr: true,
		},
		{
			name:    "too few fields",
			content: "a\n",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseTable(tc.content, tc.noFillHTTPS)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d entries, want %d: %v", len(got), len(tc.want), got)
			}
			for key, want := range tc.want {
				if got[key] != want {
					t.Errorf("got %q for key %q, want %q", got[key], key, want)
				}
			}
		})
	}
}

func TestSplitScopeKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path  string
		scope string
		key   string
		ok    bool
	}{
		{path: "/s/foo", scope: "s", key: "foo", ok: true},
		{path: "/s/foo/bar", scope: "s", key: "foo", ok: true},
		{path: "/s/", scope: "s", key: "", ok: true},
		{path: "//foo", scope: "", key: "foo", ok: true},
		{path: "/a//b", scope: "a", key: "", ok: true},
		{path: "/s", ok: false},
		{path: "/", ok: false},
		{path: "", ok: false},
	}

	for _, tc := range tests {
		scope, key, ok := splitScopeKey(tc.path)
		if ok != tc.ok || scope != tc.scope || key != tc.key {
			t.Errorf("splitScopeKey(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.path, scope, key, ok, tc.scope, tc.key, tc.ok)
		}
	}
}

func TestNullStringHeader(t *testing.T) {
	t.Parallel()

	t.Run("absent", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
		got, ok := nullStringHeader(r, "X-Test")
		if !ok || got != nil {
			t.Fatalf("got (%v, %v), want (nil, true)", got, ok)
		}
	})

	t.Run("empty value is present", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
		r.Header["X-Test"] = []string{""}
		got, ok := nullStringHeader(r, "X-Test")
		if !ok || got == nil || *got != "" {
			t.Fatalf("got (%v, %v), want (\"\", true)", got, ok)
		}
	})

	t.Run("first value wins", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
		r.Header.Add("X-Test", "one")
		r.Header.Add("X-Test", "two")
		got, ok := nullStringHeader(r, "X-Test")
		if !ok || got == nil || *got != "one" {
			t.Fatalf("got (%v, %v), want (\"one\", true)", got, ok)
		}
	})

	t.Run("tab is allowed", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
		r.Header.Set("X-Test", "a\tb")
		got, ok := nullStringHeader(r, "X-Test")
		if !ok || got == nil || *got != "a\tb" {
			t.Fatalf("got (%v, %v), want (\"a\\tb\", true)", got, ok)
		}
	})

	t.Run("non-ascii is rejected", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
		r.Header["X-Test"] = []string{"caf\xc3\xa9"}
		if got, ok := nullStringHeader(r, "X-Test"); ok || got != nil {
			t.Fatalf("got (%v, %v), want (nil, false)", got, ok)
		}
	})
}

func TestRemoteFrom(t *testing.T) {
	t.Parallel()

	t.Run("without xff", func(t *testing.T) {
		got := remoteFrom(nil, "127.0.0.1:5555")
		want := []string{"127.0.0.1:5555"}
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("with xff", func(t *testing.T) {
		xff := "203.0.113.7, 10.0.0.2"
		got := remoteFrom(&xff, "127.0.0.1:5555")
		want := []string{"203.0.113.7", " 10.0.0.2", "127.0.0.1:5555"}
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("got %v, want %v", got, want)
		}
	})
}

func TestLogEventFormat(t *testing.T) {
	t.Parallel()

	h := &Handler{
		ReqIDHeader: "X-Request-ID",
		state: map[string]scopeState{
			"s": {
				URL: "./table",
				Map: map[string]string{"k": "https://example.com/<x>&y"},
			},
		},
	}

	wantInit := `{"time":123,"event":{"type":"Init","data":{"ver":"0.1.0","state":{"s":{"url":"./table","map":{"k":"https://example.com/<x>&y"}}},"req_id_header":"X-Request-ID"}}}`
	if got := h.initEventLineAt(123); got != wantInit {
		t.Fatalf("Init event mismatch:\n got: %s\nwant: %s", got, wantInit)
	}

	xff := "203.0.113.7"
	reqID := "rid"
	ua := "a<b>&c"
	wantRequest := `{"time":456,"event":{"type":"Request","data":{"from":["203.0.113.7","127.0.0.1:5555"],"req_id":"rid","ua":"a<b>&c","scope":"s","key":"k","inner":{"type":"Get","data":{"hit":true}}}}}`
	if got := h.requestEventLine(456, &xff, &reqID, &ua, "127.0.0.1:5555", "s", "k", true); got != wantRequest {
		t.Fatalf("Request event mismatch:\n got: %s\nwant: %s", got, wantRequest)
	}

	wantMiss := `{"time":789,"event":{"type":"Request","data":{"from":["127.0.0.1:5555"],"req_id":null,"ua":null,"scope":"s","key":"missing","inner":{"type":"Get","data":{"hit":false}}}}}`
	if got := h.requestEventLine(789, nil, nil, nil, "127.0.0.1:5555", "s", "missing", false); got != wantMiss {
		t.Fatalf("Request event mismatch:\n got: %s\nwant: %s", got, wantMiss)
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		handler Handler
		wantErr bool
	}{
		{name: "valid", handler: Handler{Scopes: []ScopeConfig{{Name: "s", Source: "./f"}}}},
		{name: "no scopes", handler: Handler{}, wantErr: true},
		{name: "empty name", handler: Handler{Scopes: []ScopeConfig{{Source: "./f"}}}, wantErr: true},
		{name: "slash in name", handler: Handler{Scopes: []ScopeConfig{{Name: "a/b", Source: "./f"}}}, wantErr: true},
		{name: "empty source", handler: Handler{Scopes: []ScopeConfig{{Name: "s"}}}, wantErr: true},
		{
			name:    "duplicate",
			handler: Handler{Scopes: []ScopeConfig{{Name: "s", Source: "./f"}, {Name: "s", Source: "./g"}}},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.handler.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestProvisionFromFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "table")
	if err := os.WriteFile(path, []byte("a example.com/a\nb http://example.com/b\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	core, logs := observer.New(zapcore.InfoLevel)
	h := &Handler{
		Scopes:      []ScopeConfig{{Name: "s", Source: path}},
		ReqIDHeader: "X-Request-ID",
	}
	if err := h.provision(zap.New(core), nil); err != nil {
		t.Fatalf("Provision() error: %v", err)
	}

	if got := h.state["s"].Map["a"]; got != "https://example.com/a" {
		t.Errorf("state map a = %q, want %q", got, "https://example.com/a")
	}
	if got := h.state["s"].URL; got != path {
		t.Errorf("state url = %q, want %q", got, path)
	}

	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("got %d log entries, want 1", len(entries))
	}
	if !strings.Contains(entries[0].Message, `"type":"Init"`) {
		t.Errorf("Init event not logged: %s", entries[0].Message)
	}
}

func TestProvisionFromURL(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("remote example.com/remote\n"))
	}))
	defer server.Close()

	h := &Handler{Scopes: []ScopeConfig{{Name: "remote", Source: server.URL}}}
	if err := h.provision(zap.NewNop(), server.Client()); err != nil {
		t.Fatalf("Provision() error: %v", err)
	}
	if got := h.state["remote"].Map["remote"]; got != "https://example.com/remote" {
		t.Errorf("state map remote = %q", got)
	}
}

func TestProvisionErrors(t *testing.T) {
	t.Parallel()

	t.Run("missing file", func(t *testing.T) {
		h := &Handler{Scopes: []ScopeConfig{{Name: "s", Source: filepath.Join(t.TempDir(), "missing")}}}
		if err := h.provision(zap.NewNop(), nil); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("bad table", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "table")
		if err := os.WriteFile(path, []byte("only-one-field\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		h := &Handler{Scopes: []ScopeConfig{{Name: "s", Source: path}}}
		if err := h.provision(zap.NewNop(), nil); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("invalid utf8", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "table")
		if err := os.WriteFile(path, []byte("key http://example.com/\xff\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		h := &Handler{Scopes: []ScopeConfig{{Name: "s", Source: path}}}
		if err := h.provision(zap.NewNop(), nil); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("http status", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		h := &Handler{Scopes: []ScopeConfig{{Name: "s", Source: server.URL}}}
		if err := h.provision(zap.NewNop(), server.Client()); err == nil {
			t.Fatal("expected error")
		}
	})
}

func newServeHandler(t *testing.T) *Handler {
	t.Helper()
	return &Handler{
		Scopes: []ScopeConfig{{Name: "s", Source: "./table"}},
		state: map[string]scopeState{
			"s": {
				URL: "./table",
				Map: map[string]string{"k": "https://example.com/"},
			},
		},
		logger: zap.NewNop(),
	}
}

func TestServeHTTP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		method      string
		path        string
		headers     map[string]string
		returnValue bool
		wantStatus  int
		wantLoc     string
		wantBody    string
		wantCT      string
	}{
		{
			name:       "hit",
			method:     http.MethodGet,
			path:       "/s/k",
			wantStatus: http.StatusTemporaryRedirect,
			wantLoc:    "https://example.com/",
		},
		{
			name:       "hit with extra segment",
			method:     http.MethodGet,
			path:       "/s/k/extra",
			wantStatus: http.StatusTemporaryRedirect,
			wantLoc:    "https://example.com/",
		},
		{
			name:       "scope miss",
			method:     http.MethodGet,
			path:       "/nope/k",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "key miss",
			method:     http.MethodGet,
			path:       "/s/missing",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "bare slash",
			method:     http.MethodGet,
			path:       "/",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "one segment",
			method:     http.MethodGet,
			path:       "/s",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "wrong method",
			method:     http.MethodPost,
			path:       "/s/k",
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name:       "head is not allowed",
			method:     http.MethodHead,
			path:       "/s/k",
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name:       "invalid xff",
			method:     http.MethodGet,
			path:       "/s/k",
			headers:    map[string]string{"X-Forwarded-For": "bad\x80value"},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:        "return value",
			method:      http.MethodGet,
			path:        "/s/k",
			returnValue: true,
			wantStatus:  http.StatusOK,
			wantBody:    "https://example.com/",
			wantCT:      "text/plain; charset=utf-8",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newServeHandler(t)
			h.ReturnValue = tc.returnValue

			r := httptest.NewRequest(tc.method, "http://example.com"+tc.path, nil)
			for k, v := range tc.headers {
				r.Header.Set(k, v)
			}
			w := httptest.NewRecorder()

			if err := h.ServeHTTP(w, r, nil); err != nil {
				t.Fatalf("ServeHTTP() error: %v", err)
			}
			res := w.Result()
			defer res.Body.Close()

			if res.StatusCode != tc.wantStatus {
				t.Errorf("status = %d, want %d", res.StatusCode, tc.wantStatus)
			}
			if tc.wantLoc != "" && res.Header.Get("Location") != tc.wantLoc {
				t.Errorf("Location = %q, want %q", res.Header.Get("Location"), tc.wantLoc)
			}
			if tc.wantLoc == "" && res.Header.Get("Location") != "" {
				t.Errorf("unexpected Location = %q", res.Header.Get("Location"))
			}
			if tc.wantCT != "" && res.Header.Get("Content-Type") != tc.wantCT {
				t.Errorf("Content-Type = %q, want %q", res.Header.Get("Content-Type"), tc.wantCT)
			}
			if got := w.Body.String(); got != tc.wantBody {
				t.Errorf("body = %q, want %q", got, tc.wantBody)
			}
		})
	}
}

func TestServeHTTPLogsRequest(t *testing.T) {
	t.Parallel()

	core, logs := observer.New(zapcore.InfoLevel)
	h := newServeHandler(t)
	h.ReqIDHeader = "X-Request-ID"
	h.logger = zap.New(core)

	r := httptest.NewRequest(http.MethodGet, "http://example.com/s/k", nil)
	r.Header.Set("X-Forwarded-For", "203.0.113.7")
	r.Header.Set("X-Request-ID", "rid-1")
	r.Header.Set("User-Agent", "test-agent")
	w := httptest.NewRecorder()

	if err := h.ServeHTTP(w, r, nil); err != nil {
		t.Fatal(err)
	}

	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("got %d log entries, want 1", len(entries))
	}
	msg := entries[0].Message
	for _, want := range []string{
		`"type":"Request"`,
		`"from":["203.0.113.7","192.0.2.1:1234"]`,
		`"req_id":"rid-1"`,
		`"ua":"test-agent"`,
		`"scope":"s"`,
		`"key":"k"`,
		`"hit":true`,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("log message %s does not contain %s", msg, want)
		}
	}
}

func TestServeHTTPDoesNotLogUnmatchedShape(t *testing.T) {
	t.Parallel()

	core, logs := observer.New(zapcore.InfoLevel)
	h := newServeHandler(t)
	h.logger = zap.New(core)

	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "http://example.com/", nil),
		httptest.NewRequest(http.MethodPost, "http://example.com/s/k", nil),
	} {
		w := httptest.NewRecorder()
		if err := h.ServeHTTP(w, req, nil); err != nil {
			t.Fatal(err)
		}
	}

	if got := logs.Len(); got != 0 {
		t.Fatalf("got %d log entries, want 0", got)
	}
}

func TestUnmarshalCaddyfile(t *testing.T) {
	t.Parallel()

	t.Run("full block", func(t *testing.T) {
		d := caddyfile.NewTestDispenser(`redirector {
			scope links ./example-redirect
			scope lookup https://example.com/lookup
			req_id_header X-Request-ID
			return_value
			no_fill_https
		}`)
		var h Handler
		if err := h.UnmarshalCaddyfile(d); err != nil {
			t.Fatalf("UnmarshalCaddyfile() error: %v", err)
		}
		if len(h.Scopes) != 2 {
			t.Fatalf("got %d scopes, want 2", len(h.Scopes))
		}
		if h.Scopes[0] != (ScopeConfig{Name: "links", Source: "./example-redirect"}) {
			t.Errorf("unexpected first scope: %+v", h.Scopes[0])
		}
		if h.Scopes[1] != (ScopeConfig{Name: "lookup", Source: "https://example.com/lookup"}) {
			t.Errorf("unexpected second scope: %+v", h.Scopes[1])
		}
		if h.ReqIDHeader != "X-Request-ID" {
			t.Errorf("ReqIDHeader = %q", h.ReqIDHeader)
		}
		if !h.ReturnValue || !h.NoFillHTTPS {
			t.Errorf("flags not set: return_value=%v no_fill_https=%v", h.ReturnValue, h.NoFillHTTPS)
		}
	})

	tests := []struct {
		name  string
		input string
	}{
		{name: "no scopes", input: "redirector {\n}\n"},
		{name: "unknown subdirective", input: "redirector {\n\tscope a ./a\n\tbogus\n}\n"},
		{name: "missing source", input: "redirector {\n\tscope a\n}\n"},
		{name: "extra source", input: "redirector {\n\tscope a ./a ./b\n}\n"},
		{name: "duplicate scope", input: "redirector {\n\tscope a ./a\n\tscope a ./b\n}\n"},
		{name: "positional argument", input: "redirector foo {\n\tscope a ./a\n}\n"},
		{name: "flag argument", input: "redirector {\n\tscope a ./a\n\treturn_value yes\n}\n"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := caddyfile.NewTestDispenser(tc.input)
			var h Handler
			if err := h.UnmarshalCaddyfile(d); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestRawEncoder(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	enc := &RawEncoder{Encoder: zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig())}
	logger := zap.New(zapcore.NewCore(enc, zapcore.AddSync(&buf), zapcore.InfoLevel))
	logger.Info(`{"time":123,"event":{"type":"Request"}}`, zap.String("ignored", "field"))

	want := "{\"time\":123,\"event\":{\"type\":\"Request\"}}\n"
	if got := buf.String(); got != want {
		t.Fatalf("raw log = %q, want %q", got, want)
	}

	if id := (RawEncoder{}).CaddyModule().ID; id != "caddy.logging.encoders.redirector" {
		t.Errorf("module ID = %q", id)
	}

	d := caddyfile.NewTestDispenser("redirector")
	var e RawEncoder
	if err := e.UnmarshalCaddyfile(d); err != nil {
		t.Errorf("UnmarshalCaddyfile() error: %v", err)
	}
}
