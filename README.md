# Caddy HTTP Redirector

[中文](README.zh.md) | **English**

A Caddy v2 HTTP handler module that redirects `/{scope}/{key}` requests to links from per-scope key/link tables.

- Module ID: `http.handlers.redirector`
- Caddyfile directive: `redirector`
- Target: Caddy v2.11.9, Go 1.26+

## Features

- Loads a key/link table per scope from a local file or an `http(s)://` URL.
- Returns `307` and `Location` on a hit; `return_value` can return the mapped value directly instead.
- Returns `404` on a miss and `405` for non-GET requests.
- Emits line-delimited JSON event logs (`Init`, `Request`).
- Supports a dedicated Caddy-managed log file with one complete JSON event per line.
- Loads configuration when Caddy provisions the module; it does not expose a request-triggered update endpoint.

## Build

With [xcaddy](https://github.com/caddyserver/xcaddy):

```sh
xcaddy build --with github.com/stackinspector/caddy-http-redirector
```

To build from this checkout without fetching the module remotely, use `=` to point at the current directory:

```sh
xcaddy build --with github.com/stackinspector/caddy-http-redirector=.
```

This repository also includes a local test entry point in `cmd/caddy`:

```sh
go build -o caddy ./cmd/caddy
./caddy run --config Caddyfile.example --adapter caddyfile
```

## Caddyfile syntax

```caddyfile
redirector [<matcher>] {
	scope <name> <source>
	req_id_header <header-name>
	return_value
	no_fill_https
}
```

- `scope`: repeatable. `<name>` is the first path segment of the URL; `<source>` is a local file path or an `http(s)://` URL. The config is read once when Caddy loads the configuration.
- `req_id_header`: optional. Names a request header whose value is recorded in the `req_id` field of request log events.
- `return_value`: optional. On a hit, returns the mapped value itself (`200`, `text/plain; charset=utf-8`) instead of a `307` redirect. Useful for simple lookup/mapping services.
- `no_fill_https`: optional. By default, a link that does not start with `http://` or `https://` gets an `https://` prefix. Enabling this disables that behavior.

`redirector` is ordered before `respond` by default. It responds directly and does not call the next handler; place it at the site root or use a matcher / `handle`.

### Redirect service

```caddyfile
:8080 {
	redirector {
		scope links ./example-redirect
		req_id_header X-Request-ID
	}
}
```

```sh
curl -i http://localhost:8080/links/home
# HTTP/1.1 307 Temporary Redirect
# Location: https://example.com/
```

### Lookup/mapping service

```caddyfile
:8081 {
	redirector {
		scope lookup ./example-lookup
		return_value
		no_fill_https
	}
}
```

```sh
curl http://localhost:8081/lookup/alice@example.com
# Alice
```

### Table file format

Each line contains one `<key> <target>` pair separated by whitespace; blank lines are ignored:

```text
home            example.com/
docs            docs.example.com/
http-example    http://example.com/http
https-example   https://example.com/https
```

- Targets without a scheme get an `https://` prefix; `http://` and `https://` targets are left unchanged. `no_fill_https` disables prefixing entirely.
- Duplicate keys cause Caddy configuration loading to fail.
- A line that does not have exactly two fields, or a config that is not valid UTF-8, also causes loading to fail.
- Remote config sources must return `200`; reads are limited to 10 MiB and the HTTP timeout is 30 seconds.
- Relative paths resolve relative to the **Caddy process working directory**, not the Caddyfile's directory. Absolute paths are recommended in production.

## Logging

Logs consist of one JSON event per line. There are two event types: `Init` (when the configuration is loaded) and `Request` (for each GET request that yields `{scope}/{key}`). Requests without two path segments (e.g. `/`) and non-GET requests do not produce request log entries.

### Dedicated log file (recommended)

Caddy's logging app supports named logs, file output, and filtering by logger name. This module registers an encoder that writes only the message itself (`format redirector`), so events can go to a dedicated file as one complete JSON event per line while Caddy handles file rotation:

```caddyfile
{
	log redirector {
		output file /var/log/caddy/redirector.log
		format redirector
		include http.handlers.redirector
	}
	log default {
		exclude http.handlers.redirector
	}
}
```

- `include http.handlers.redirector` sends only this module's logs to the file.
- `log default { exclude ... }` prevents the same events from also going to the default log.
- The file writer supports Caddy's `roll_size`, `roll_keep`, and similar options as needed.

### Fallback to Caddy's log

If the named log above is not configured, `Init` and `Request` events go to Caddy's default log with the logger name `http.handlers.redirector`. In that case Caddy wraps the event JSON in its own JSON envelope (for example `level`, `ts`, `msg`). For one complete event per line, use the dedicated log configuration; to write to stdout, change the named log's `output` to `stdout`.

### Event examples

```json
{"time":1795069785320,"event":{"type":"Init","data":{"ver":"0.1.0","state":{"links":{"url":"./example-redirect","map":{"home":"https://example.com/"}}},"req_id_header":"X-Request-ID"}}}
{"time":1795069788245,"event":{"type":"Request","data":{"from":["203.0.113.7"," 10.0.0.2","127.0.0.1:55825"],"req_id":"abc-123","ua":"curl/8.7.1","scope":"links","key":"home","inner":{"type":"Get","data":{"hit":true}}}}}
```

- `time`: Unix timestamp in milliseconds.
- `from`: each `X-Forwarded-For` entry (spaces within entries are preserved), followed by the immediate peer's `host:port`.
- `req_id`: value of the header named by `req_id_header`; `null` if not configured or absent from the request.
- `ua`: `User-Agent`; `null` if absent.
- `hit`: whether the key was found in the scope's table.

## Behavior details

- Only `GET` is accepted. Other methods (including `HEAD`) return `405`.
- Paths are parsed as `/{scope}/{key}`; extra path segments are ignored. For example, `/links/home/extra` uses `scope=links` and `key=home`.
- Hits return `307` and `Location`; misses return `404`.
- Returns `400` if `X-Forwarded-For`, `User-Agent`, or the configured request ID header contains bytes that are not visible ASCII.
- The handler responds directly to every request that reaches it and does not call the next handler.
- Configuration is read only when Caddy loads or reloads configuration. After changing a table file or remote config, reload Caddy. Because table contents are not part of the JSON adapted from the Caddyfile, Caddy usually considers the configuration unchanged, so `--force` is required:

  ```sh
  caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile --force
  ```

  This module does not provide a request-triggered config update endpoint; updates require someone with permission to reload Caddy.

## Testing

```sh
go test ./...
```

Tests cover table parsing, path parsing, request handling, log event formatting, Caddyfile parsing and adaptation, remote/local config loading, and the custom raw encoder.

## License

MPL-2.0. See [LICENSE](LICENSE).
