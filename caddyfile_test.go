package redirector

import (
	"encoding/json"
	"testing"

	"github.com/caddyserver/caddy/v2/caddyconfig"
	_ "github.com/caddyserver/caddy/v2/modules/logging"
)

type adaptedHandler struct {
	Handler     string        `json:"handler"`
	Scopes      []ScopeConfig `json:"scopes"`
	ReqIDHeader string        `json:"req_id_header"`
	ReturnValue bool          `json:"return_value"`
	NoFillHTTPS bool          `json:"no_fill_https"`
}

type adaptedConfig struct {
	Apps struct {
		HTTP struct {
			Servers map[string]struct {
				Routes []struct {
					Match  []map[string]any `json:"match"`
					Handle []adaptedHandler `json:"handle"`
				} `json:"routes"`
			} `json:"servers"`
		} `json:"http"`
	} `json:"apps"`
	Logging struct {
		Logs map[string]struct {
			Include []string `json:"include"`
			Encoder struct {
				Format string `json:"format"`
			} `json:"encoder"`
			Writer struct {
				Output   string `json:"output"`
				Filename string `json:"filename"`
			} `json:"writer"`
		} `json:"logs"`
	} `json:"logging"`
}

func adaptCaddyfile(t *testing.T, input string) (adaptedConfig, error) {
	t.Helper()
	adapter := caddyconfig.GetAdapter("caddyfile")
	if adapter == nil {
		t.Fatal("caddyfile adapter is not registered")
	}
	result, _, err := adapter.Adapt([]byte(input), nil)
	if err != nil {
		return adaptedConfig{}, err
	}
	var cfg adaptedConfig
	if err := json.Unmarshal(result, &cfg); err != nil {
		t.Fatalf("decoding adapted JSON: %v\n%s", err, result)
	}
	return cfg, nil
}

func firstRedirectorHandler(t *testing.T, cfg adaptedConfig) (adaptedHandler, []map[string]any) {
	t.Helper()
	for _, server := range cfg.Apps.HTTP.Servers {
		for _, route := range server.Routes {
			for _, handler := range route.Handle {
				if handler.Handler == "redirector" {
					return handler, route.Match
				}
			}
		}
	}
	t.Fatal("no redirector handler in adapted config")
	return adaptedHandler{}, nil
}

func TestCaddyfileAdapt(t *testing.T) {
	t.Parallel()

	cfg, err := adaptCaddyfile(t, `:8080 {
		redirector {
			scope rust ./example-redirect
			scope lookup https://example.com/lookup
			req_id_header X-Request-ID
			return_value
			no_fill_https
		}
	}`)
	if err != nil {
		t.Fatalf("adapting Caddyfile: %v", err)
	}

	handler, _ := firstRedirectorHandler(t, cfg)
	if len(handler.Scopes) != 2 {
		t.Fatalf("got %d scopes, want 2", len(handler.Scopes))
	}
	if handler.Scopes[0] != (ScopeConfig{Name: "rust", Source: "./example-redirect"}) {
		t.Errorf("unexpected first scope: %+v", handler.Scopes[0])
	}
	if handler.Scopes[1] != (ScopeConfig{Name: "lookup", Source: "https://example.com/lookup"}) {
		t.Errorf("unexpected second scope: %+v", handler.Scopes[1])
	}
	if handler.ReqIDHeader != "X-Request-ID" || !handler.ReturnValue || !handler.NoFillHTTPS {
		t.Errorf("unexpected flags: %+v", handler)
	}
}

func TestCaddyfileAdaptWithMatcher(t *testing.T) {
	t.Parallel()

	cfg, err := adaptCaddyfile(t, `:8080 {
		redirector /r/* {
			scope rust ./example-redirect
		}
	}`)
	if err != nil {
		t.Fatalf("adapting Caddyfile: %v", err)
	}
	_, match := firstRedirectorHandler(t, cfg)
	if len(match) == 0 {
		t.Fatal("expected a route matcher")
	}
}

func TestCaddyfileAdaptDedicatedLog(t *testing.T) {
	t.Parallel()

	cfg, err := adaptCaddyfile(t, `{
		log redirector {
			output file /var/log/caddy/redirector.log
			format redirector
			include http.handlers.redirector
		}
		log default {
			exclude http.handlers.redirector
		}
	}

	:8080 {
		redirector {
			scope rust ./example-redirect
		}
	}`)
	if err != nil {
		t.Fatalf("adapting Caddyfile: %v", err)
	}

	logCfg, ok := cfg.Logging.Logs["redirector"]
	if !ok {
		t.Fatal("redirector log config not found")
	}
	if logCfg.Encoder.Format != "redirector" {
		t.Errorf("encoder format = %q, want redirector", logCfg.Encoder.Format)
	}
	if logCfg.Writer.Output != "file" || logCfg.Writer.Filename != "/var/log/caddy/redirector.log" {
		t.Errorf("unexpected writer config: %+v", logCfg.Writer)
	}
	if len(logCfg.Include) != 1 || logCfg.Include[0] != "http.handlers.redirector" {
		t.Errorf("unexpected include list: %v", logCfg.Include)
	}
}

func TestCaddyfileAdaptErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{
			name: "no scopes",
			input: `:8080 {
				redirector {
				}
			}`,
		},
		{
			name: "unknown subdirective",
			input: `:8080 {
				redirector {
					scope a ./a
					bogus
				}
			}`,
		},
		{
			name: "duplicate scope",
			input: `:8080 {
				redirector {
					scope a ./a
					scope a ./b
				}
			}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := adaptCaddyfile(t, tc.input); err == nil {
				t.Fatal("expected adaptation error")
			}
		})
	}
}
