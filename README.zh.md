# Caddy HTTP Redirector

**中文** | [English](README.md)

一个 Caddy v2 HTTP handler 模块，根据每个 scope 的 key/link 表把 `/{scope}/{key}` 请求重定向到对应链接。

- 模块 ID：`http.handlers.redirector`
- Caddyfile 指令：`redirector`
- 目标版本：Caddy v2.11.9，Go 1.26+

## 功能

- 按 scope 加载 key/link 表，支持本地文件或 `http(s)://` URL。
- 命中返回 `307` 和 `Location`；也可开启 `return_value` 直接返回映射值。
- 未命中返回 `404`，非 GET 请求返回 `405`。
- 输出逐行 JSON 事件日志（`Init`、`Request`）。
- 日志可写入 Caddy 管理的独立文件，保持每行一个完整 JSON 事件。
- 配置在 Caddy Provision 时加载；不提供通过 HTTP 请求触发更新的接口。

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
home            example.com/
docs            docs.example.com/
http-example    http://example.com/http
https-example   https://example.com/https
```

- 未带 scheme 的 target 默认补 `https://`；`http://` 和 `https://` 开头的不变。也可以开启 `no_fill_https` 完全禁用补全。
- 重复 key 会导致 Caddy 加载配置失败。
- 单行不是恰好两个字段、或配置不是合法 UTF-8，也会导致加载失败。
- 远程配置源必须返回 `200`；读取上限为 10 MiB，HTTP 超时为 30 秒。
- 相对路径相对于 **Caddy 进程的工作目录**，不是 Caddyfile 所在目录。生产环境建议使用绝对路径。

## 日志

日志采用每行一个 JSON 事件的形式，共两种事件：`Init`（配置加载时）和 `Request`（每个成功解析出 `{scope}/{key}` 的 GET 请求）。未解析出两段路径的请求（如 `/`）和非 GET 请求不会写请求日志。

### 独立日志文件（推荐）

Caddy 的 logging app 支持命名日志、文件输出和按 logger 名称过滤。本模块注册了一个只输出消息本身的 encoder（`format redirector`），因此可以把日志写进独立文件，同时保持每行一个完整 JSON 事件，并由 Caddy 负责文件轮转：

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
- 文件 writer 支持 Caddy 的 `roll_size`、`roll_keep` 等选项，可按需添加。

### 回退到 Caddy 日志

如果不配置上面的命名日志，`Init` 和 `Request` 事件会写入 Caddy 的默认日志，logger 名称为 `http.handlers.redirector`。此时 Caddy 会在事件 JSON 外面再套一层自己的 JSON 信封（例如 `level`、`ts`、`msg`）。如果需要每行一个完整事件，请使用上面的独立日志配置；如果需要写到 stdout，把命名 log 的 `output` 改成 `stdout` 即可。

### 事件示例

```json
{"time":1795069785320,"event":{"type":"Init","data":{"ver":"0.1.0","state":{"links":{"url":"./example-redirect","map":{"home":"https://example.com/"}}},"req_id_header":"X-Request-ID"}}}
{"time":1795069788245,"event":{"type":"Request","data":{"from":["203.0.113.7"," 10.0.0.2","127.0.0.1:55825"],"req_id":"abc-123","ua":"curl/8.7.1","scope":"links","key":"home","inner":{"type":"Get","data":{"hit":true}}}}}
```

- `time`：Unix 毫秒时间戳。
- `from`：`X-Forwarded-For` 的各个条目（保留条目中的空格）后接直连对端的 `host:port`。
- `req_id`：`req_id_header` 指定头的值；未配置或请求未携带时为 `null`。
- `ua`：`User-Agent`；未携带时为 `null`。
- `hit`：是否在对应 scope 的表中找到 key。

## 行为细节

- 只接受 `GET`。其他方法（包括 `HEAD`）返回 `405`。
- 路径按 `/{scope}/{key}` 解析；多余路径段被忽略。例如 `/links/home/extra` 使用 `scope=links`、`key=home`。
- 命中返回 `307` 和 `Location`；未命中返回 `404`。
- `X-Forwarded-For`、`User-Agent` 或配置的请求 ID 头包含非可见 ASCII 字节时返回 `400`。
- 该 handler 对进入它的所有请求都会直接响应，不会继续调用后续 handler。
- 配置只在 Caddy 加载/重载配置时读取。更新表文件或远程配置后需要重载；由于表内容不在 Caddyfile 适配出的 JSON 中，Caddy 通常认为配置“未变化”，因此需要加 `--force`：

  ```sh
  caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile --force
  ```

  本模块不提供通过请求触发配置更新的接口；更新只能由有权限 reload Caddy 的人完成。

## 测试

```sh
go test ./...
```

测试覆盖表解析、路径解析、请求处理、日志事件格式、Caddyfile 解析与适配、远程/本地配置加载，以及自定义 raw encoder 的输出。

## 许可证

MPL-2.0，见 [LICENSE](LICENSE)。
