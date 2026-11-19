# Caddy HTTP Redirector

[`http-redirector`](https://github.com/stackinspector/http-redirector) 的 Caddy v2 移植版。它是一个 HTTP handler 模块（模块 ID：`http.handlers.redirector`，Caddyfile 指令：`redirector`），根据每个 scope 的 key/link 表把 `/{scope}/{key}` 请求重定向到对应链接。

当前版本针对 **Caddy v2.11.9** 及 Go 1.26+ 构建。

## 与 Rust 原版的对应关系

| Rust 原版 | 本模块 |
| --- | --- |
| 独立进程、`-p/--port` | Caddy site address（如 `:8080`） |
| `-c/--configs "scope,url;..."` | 可重复的 `scope <name> <source>` 子指令 |
| `-l/--log-path` | Caddy 的全局 `log` 块（`output file ...` + `format redirector`） |
| `-h/--req-id-header` | `req_id_header <header>` |
| `-v/--return-value` | `return_value` |
| `-f/--no-fill-https` | `no_fill_https` |
| `-k/--update-key`、`-u/--allow-update` | **已移除**；更新配置请 reload Caddy |
| 通过 `/{update_key}/{scope}` 触发更新 | **已移除**，无更新端点 |

## 构建

使用 [xcaddy](https://github.com/caddyserver/xcaddy)：

```sh
xcaddy build --with github.com/stackinspector/caddy-http-redirector
```

在本仓库内构建（不依赖远程模块）时，用 `=` 指向当前目录：

```sh
xcaddy build --with github.com/stackinspector/caddy-http-redirector=.
```

本仓库也带有一个便于本地测试的入口 `cmd/caddy`：

```sh
go build -o caddy ./cmd/caddy
./caddy run --config Caddyfile.example --adapter caddyfile
```

## Caddyfile 语法

```caddyfile
redirector [<matcher>] {
	scope <name> <source>
	req_id_header <header-name>
	return_value
	no_fill_https
}
```

- `scope`：可重复。`<name>` 是 URL 中的第一段路径；`<source>` 是本地文件路径或 `http(s)://` URL。配置在 Caddy 加载配置时读取一次。
- `req_id_header`：可选。指定一个请求头名称，其值会写入请求日志的 `req_id` 字段。
- `return_value`：可选。命中时返回映射值本身（`200`，`text/plain; charset=utf-8`），而不是 `307` 重定向。适合做简单的查询/映射服务。
- `no_fill_https`：可选。默认情况下，表中的链接如果不以 `http://` 或 `https://` 开头，会自动补上 `https://`。开启后不补。

`redirector` 默认排在 `respond` 之前。它在处理请求后直接响应，不会调用后续 handler；因此通常放在站点根部，或配合 matcher / `handle` 使用。

### 重定向服务

```caddyfile
:8080 {
	redirector {
		scope rust ./example-redirect
		req_id_header X-Request-ID
	}
}
```

```sh
curl -i http://localhost:8080/rust/rust
# HTTP/1.1 307 Temporary Redirect
# Location: https://www.rust-lang.org/
```

### 查询/映射服务

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

### 表文件格式

每行一个 `<key> <target>`，以空白分隔，空行会被忽略：

```text
rust            www.rust-lang.org/
crates          crates.io/
http-example    http://example.com/
https-example   https://example.com/
```

- 未带 scheme 的 target 默认补 `https://`；`http://` 和 `https://` 开头的不变。也可以开启 `no_fill_https` 完全禁用补全。
- 重复 key 会导致 Caddy 加载配置失败。
- 单行不是恰好两个字段、或配置不是合法 UTF-8，也会导致加载失败。
- 远程配置源必须返回 `200`；读取上限为 10 MiB，HTTP 超时为 30 秒。
- 相对路径相对于 **Caddy 进程的工作目录**，不是 Caddyfile 所在目录。生产环境建议使用绝对路径。

## 日志

日志格式与 Rust 原版保持一致：每行一个 JSON 事件，共两种事件：`Init`（配置加载时）和 `Request`（每个成功解析出 `{scope}/{key}` 的 GET 请求）。未解析出两段路径的请求（如 `/`）和非 GET 请求不会写请求日志，与原版一致。

### 独立日志文件（推荐）

Caddy 的 logging app 支持命名日志、文件输出和按 logger 名称过滤。本模块额外注册了一个只输出消息本身的 encoder（`format redirector`），因此可以把日志写进独立文件，同时保持原版的逐行 JSON 格式，并由 Caddy 负责文件轮转：

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

- `include http.handlers.redirector` 只让本模块的日志进入该文件。
- `log default { exclude ... }` 避免同样的日志又进入默认日志。
- 不想让日志进入默认日志时，这一条是必要的。
- 文件 writer 支持 Caddy 的 `roll_size`、`roll_keep` 等选项，可按需添加。

### 回退到 Caddy 日志

如果不配置上面的命名日志，`Init` 和 `Request` 事件会写入 Caddy 的默认日志，logger 名称为 `http.handlers.redirector`。此时 Caddy 会在事件 JSON 外面再套一层自己的 JSON 信封（例如 `level`、`ts`、`msg`）。如果需要严格保持原版格式，请使用上面的独立日志配置；如果需要写到 stdout，把命名 log 的 `output` 改成 `stdout` 即可。

### 事件示例

```json
{"time":1795069785320,"event":{"type":"Init","data":{"ver":"0.1.0","state":{"rust":{"url":"./example-redirect","map":{"rust":"https://www.rust-lang.org/"}}},"req_id_header":"X-Request-ID","update_key":null,"allow_update":false}}}
{"time":1795069788245,"event":{"type":"Request","data":{"from":["203.0.113.7"," 10.0.0.2","127.0.0.1:55825"],"req_id":"abc-123","ua":"curl/8.7.1","scope":"rust","key":"trpl","inner":{"type":"Get","data":{"hit":true}}}}}
```

- `time`：Unix 毫秒时间戳。
- `from`：`X-Forwarded-For` 的各个条目（按原样、保留空格）后接直连对端的 `host:port`。
- `req_id`：`req_id_header` 指定头的值；未配置或请求未携带时为 `null`。
- `ua`：`User-Agent`；未携带时为 `null`。
- `hit`：是否在对应 scope 的表中找到 key。
- `update_key` / `allow_update`：为了兼容原版日志 schema 保留，但固定为 `null` / `false`；更新功能已移除。

## 行为细节

- 只接受 `GET`。其他方法（包括 `HEAD`）返回 `405`，与原版代码一致。
- 路径按 `/{scope}/{key}` 解析；多余路径段被忽略。例如 `/rust/foo/bar` 使用 `scope=rust`、`key=foo`。
- 命中返回 `307` 和 `Location`；未命中返回 `404`。
- `X-Forwarded-For`、`User-Agent` 或配置的请求 ID 头包含非可见 ASCII 字节时返回 `400`。
- 该 handler 对进入它的所有请求都是终结性的，不会继续调用后续 handler。
- 配置只在 Caddy 加载/重载配置时读取。更新表文件或远程配置后需要重载；由于表内容不在 Caddyfile 适配出的 JSON 中，Caddy 通常认为配置“未变化”，因此需要加 `--force`：

  ```sh
  caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile --force
  ```

  这也是移除原版 `/{update_key}/{scope}` 更新端点后的替代方式：不再允许任意请求触发重新抓取，更新只能由有权限 reload Caddy 的人完成。

## 测试

```sh
go test ./...
```

测试覆盖表解析、路径解析、请求处理、日志事件格式、Caddyfile 解析与适配、远程/本地配置加载，以及自定义 raw encoder 的输出。

## 许可证

MIT，见 [LICENSE](LICENSE)。原项目版权归 stackinspector 所有。
