// Command caddy builds a Caddy server with the redirector module compiled in.
// It is only for local development and testing; production builds should use
// xcaddy with this module:
//
//	xcaddy build --with github.com/stackinspector/caddy-http-redirector
package main

import (
	caddycmd "github.com/caddyserver/caddy/v2/cmd"
	_ "github.com/caddyserver/caddy/v2/modules/standard"
	_ "github.com/stackinspector/caddy-http-redirector"
)

func main() {
	caddycmd.Main()
}
