package redirector

import (
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

func init() {
	httpcaddyfile.RegisterHandlerDirective("redirector", parseCaddyfile)
	httpcaddyfile.RegisterDirectiveOrder("redirector", httpcaddyfile.Before, "respond")
}

// parseCaddyfile unmarshals the handler from Caddyfile tokens.
func parseCaddyfile(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	var handler Handler
	if err := handler.UnmarshalCaddyfile(h.Dispenser); err != nil {
		return nil, err
	}
	return &handler, nil
}

// UnmarshalCaddyfile parses the following syntax:
//
//	redirector [<matcher>] {
//	    scope <name> <url-or-path>
//	    req_id_header <header-name>
//	    return_value
//	    no_fill_https
//	}
func (h *Handler) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	d.Next() // consume directive name

	if d.NextArg() {
		return d.ArgErr()
	}

	for d.NextBlock(0) {
		switch d.Val() {
		case "scope":
			var name, source string
			if !d.AllArgs(&name, &source) {
				return d.ArgErr()
			}
			for _, existing := range h.Scopes {
				if existing.Name == name {
					return d.Errf("duplicate scope %q", name)
				}
			}
			h.Scopes = append(h.Scopes, ScopeConfig{Name: name, Source: source})

		case "req_id_header":
			var name string
			if !d.AllArgs(&name) {
				return d.ArgErr()
			}
			if h.ReqIDHeader != "" {
				return d.Err("req_id_header already specified")
			}
			h.ReqIDHeader = name

		case "return_value":
			if d.NextArg() {
				return d.ArgErr()
			}
			h.ReturnValue = true

		case "no_fill_https":
			if d.NextArg() {
				return d.ArgErr()
			}
			h.NoFillHTTPS = true

		default:
			return d.Errf("unrecognized subdirective %q", d.Val())
		}
	}

	return h.Validate()
}
