# go-req

`go-req`（package `req`）是一个轻量级、标准库优先的 HTTP Client 请求库。

它不是对 `net/http` 的重新实现，而是在标准库之上提供更短的请求构建路径。原生 `http.Client`、`http.Transport`、`http.RoundTripper`、`DialContext`、`Proxy`、`TLSClientConfig`、`CookieJar`、`CheckRedirect` 等能力全部保留。

设计原则：

> **Simple things should be simple, complex things should be possible.**

- 简单请求只需要 2～4 行代码。
- 不包装响应类型，始终返回 `*http.Response`。
- 不隐藏网络行为，不自动读取或关闭 `Do` 返回的 Body。
- 不使用第三方依赖，核心实现只依赖标准库。
- 不修改 `http.DefaultClient` 或 `http.DefaultTransport`。
- 可以用原生 `http.Request` 直接调用 `Client.Do`。

要求 Go 1.21 或更高版本。

## 安装

```bash
go get github.com/Xwudao/go-req
```

```go
import "github.com/Xwudao/go-req"
```

package 名是 `req`，因此通常写作 `req.New(...)`。

## 快速开始

```go
client := req.New()

var user User
err := client.Get(ctx, "https://api.example.com/users/1").ToJSON(&user)
if err != nil {
    return err
}
```

带 BaseURL、超时和默认 Header：

```go
client := req.New(
    req.WithBaseURL("https://api.example.com"),
    req.WithTimeout(10*time.Second),
    req.WithHeader("User-Agent", "go-req"),
)
```

## GET 与 POST JSON

GET：

```go
var user User

err := client.Get(ctx, "/users/1").ToJSON(&user)
if err != nil {
    return err
}
```

POST JSON 不需要手动 `json.Marshal`：

```go
var result CreateUserResponse

err := client.Post(ctx, "/users").
    JSON(map[string]any{
        "name": "Alice",
        "age":  25,
    }).
    ToJSON(&result)
if err != nil {
    return err
}
```

`JSON` 会自动设置 `Content-Type: application/json`。在 Builder 上显式设置的 Content-Type 会覆盖该默认值，无论设置发生在 `JSON` 之前还是之后。

其他方法：

```go
client.Put(ctx, "/users/1")
client.Patch(ctx, "/users/1")
client.Delete(ctx, "/users/1")
client.Head(ctx, "/users/1")
client.NewRequest(ctx, http.MethodOptions, "/users")
```

## Query 参数

```go
var users []User

err := client.Get(ctx, "/users").
    Query("page", "1").
    Query("limit", "20").
    ToJSON(&users)
```

语义是明确区分的：

```go
// Query 覆盖同名参数，包括 URL 中已有的参数。
client.Get(ctx, "/users?page=0").Query("page", "1")

// AddQuery 追加参数。
client.Get(ctx, "/users").
    AddQuery("tag", "go").
    AddQuery("tag", "http")

// QueryValues 批量覆盖每个 key 的值；nil/空 slice 表示删除该 key。
client.Get(ctx, "/users").QueryValues(url.Values{
    "page": {"1"},
    "tags": {"go", "http"},
})
```

`AddQuery` 会保留 URL 中已有值以及其他同名 `AddQuery` 值。操作按调用顺序生效：

```go
AddQuery("x", "2").Query("x", "1") // x=1
Query("x", "1").AddQuery("x", "2") // x=1&x=2
```

## Header、Cookie 与认证

```go
err := client.Get(ctx, "/users").
    Header("Authorization", "Bearer token").
    Header("X-Request-ID", "123").
    ToJSON(&result)
```

批量复制 Header：

```go
client.Get(ctx, "/users").
    Headers(http.Header{
        "X-Trace-ID": {"abc"},
        "Accept":     {"application/json"},
    })
```

常见认证操作：

```go
client.Get(ctx, "/basic").BasicAuth("username", "password")
client.Get(ctx, "/api").BearerToken("token")
client.Get(ctx, "/page").
    Cookie(&http.Cookie{Name: "session", Value: "abc"})
```

`Header`/`Headers` 会覆盖 Builder 上同名 Header。Builder 上的值会覆盖 Client 的默认 Header，但不同请求之间不会共享 Header 状态。

`Host`、`Content-Length`、`Transfer-Encoding` 与标准库一样是特殊字段，不是普通 Header。需要覆盖 `Host` 时使用 `Build()` 取得原生请求并设置 `req.Host`。

Cookie 持久化请通过自定义 `http.Client.Jar`：

```go
jar, _ := cookiejar.New(nil)
hc := &http.Client{Jar: jar}
client := req.New(req.WithHTTPClient(hc))
```

## Form 请求

```go
err := client.Post(ctx, "/login").
    Form(url.Values{
        "username": {"Alice"},
        "password": {"secret"},
    }).
    ToJSON(&result)
```

Form 使用 `url.Values.Encode()`，Content-Type 默认为 `application/x-www-form-urlencoded`。

## 原生 http.Client

`WithHTTPClient` 直接使用调用方提供的指针，不复制、不修改：

```go
custom := &http.Client{
    Timeout:       15 * time.Second,
    CheckRedirect: customRedirect,
    Jar:           customCookieJar,
}

client := req.New(req.WithHTTPClient(custom))
```

自定义 Client 的 Transport、Jar、CheckRedirect、Timeout 行为全部保留。`client.HTTPClient()` 会返回同一个指针。

由于 `req` 不会修改外部 Client，`WithHTTPClient` 不能与 `WithTimeout`、`WithTransport`、`WithDialContext` 或任何代理 Option 组合。需要这些配置时，直接设置 `http.Client` 或 `http.Transport` 的对应字段。

## 自定义 Transport

`WithTransport` 接受任意 `http.RoundTripper`：

```go
transport := http.DefaultTransport.(*http.Transport).Clone()
transport.MaxIdleConns = 100
transport.MaxIdleConnsPerHost = 20
transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}

client := req.New(req.WithTransport(transport))
```

`req` 会创建自己的 `http.Client` 并引用该 Transport，但不会修改 Transport 的字段。Transport 的所有权和生命周期仍归调用方。

与代理 Option 组合时，传入的 Transport 必须是 `*http.Transport`。`req` 会先 `Clone()` 它，再在克隆实例上设置 `Proxy`；原 Transport 的 `TLSClientConfig`、`DialContext`、连接池等配置全部保留，调用方的 Transport 不会被修改。

`WithTransport` 可以与 `WithTimeout` 组合，超时设置在 `req` 创建的 `http.Client` 上：

```go
client := req.New(
    req.WithTransport(transport),
    req.WithTimeout(5*time.Second),
)
```

## 自定义 DialContext

```go
client := req.New(
    req.WithDialContext(func(
        ctx context.Context,
        network, address string,
    ) (net.Conn, error) {
        return customDial(ctx, network, address)
    }),
)
```

实现方式是从 `http.DefaultTransport` 克隆一个独立 `*http.Transport`，然后只修改克隆实例的 `DialContext`。全局 Transport 不会被修改。

边界说明：

- `WithDialContext` 不能与 `WithTransport` 或 `WithHTTPClient` 组合。`req` 不会对未知 Transport 做类型断言或隐式覆盖；需要组合时请直接配置 `http.Transport.DialContext`。
- `DialContext` 控制的是网络拨号，不是请求的最终目标。使用代理时，这里拨号的是代理地址。
- 已有的空闲连接不会重新拨号。
- `WithDialContext` 可以与代理 Option 组合，两者都应用到 `req` 克隆出的独立 `*http.Transport`。

## 代理配置

默认行为与标准库一致：使用 `http.ProxyFromEnvironment`，读取 `HTTP_PROXY`、`HTTPS_PROXY`、`NO_PROXY`（及对应小写形式）等环境变量。代理能力完全由 `http.Transport.Proxy` 提供，`req` 不实现 HTTP、HTTPS CONNECT 或 SOCKS5 协议本身。

固定代理：

```go
client := req.New(
    req.WithProxy("http://user:password@127.0.0.1:8080"),
)
```

支持 `http://`、`https://`、`socks5://`、`socks5h://`，以及 URL 中的用户名密码。HTTP/HTTPS 代理省略端口时使用标准库默认端口；SOCKS5 代理必须显式指定端口。

完全禁用代理，包括忽略所有代理环境变量：

```go
client := req.New(req.WithoutProxy())
```

显式恢复环境变量代理：

```go
client := req.New(req.WithProxyFromEnvironment())
```

按请求选择代理：

```go
client := req.New(req.WithProxyFunc(func(r *http.Request) (*url.URL, error) {
    if strings.HasPrefix(r.URL.Host, "internal.") {
        return nil, nil // 直连
    }
    return url.Parse("socks5h://127.0.0.1:1080")
}))
```

`WithProxyFunc` 直接使用标准库签名。函数由 `http.Transport` 在每次连接尝试时调用，可以返回 `nil` 表示直连。需要为不同请求使用完全独立的代理配置时，也可以创建多个 `Client`。

规则与边界：

- `WithProxy` 会校验 Scheme、Host 和端口：拒绝相对地址、不支持的 Scheme、空主机、无效或越界端口、SOCKS5 缺少端口，以及带 path/query/fragment 的代理 URL。
- 所有代理 Option 只在 `New` / `NewWithOptions` 初始化阶段应用。Request Builder 不会动态修改 Transport。
- 默认 Transport 是 `http.DefaultTransport` 的独立 Clone。`WithTransport` 与代理 Option 组合时会 Clone 调用方的 `*http.Transport`，不会修改原对象。
- `WithHTTPClient` 无法安全设置代理，不能与任何代理 Option 组合。`WithTransport` 使用自定义 `RoundTripper` 时也无法安全设置 `Proxy`，会返回明确的配置冲突错误；只有 `*http.Transport` 可以被 Clone 后设置代理。
- 冲突检测与 Option 顺序无关。`WithProxy`、`WithoutProxy`、`WithProxyFromEnvironment`、`WithProxyFunc` 对同一 Client 重复设置时最后一个生效。
- `WithDialContext` 可以与代理 Option 组合；`WithTransport` 与 `WithDialContext` 仍然互斥。
- Debug 不会输出代理用户名、密码或 `Proxy-Authorization` 的值；请求 URL 中的 userinfo 也会被脱敏。

## 原生 Request / Response

`Client.Do` 的签名与 `http.Client.Do` 完全一致：

```go
func (c *Client) Do(req *http.Request) (*http.Response, error)
```

```go
raw, err := http.NewRequestWithContext(
    ctx,
    http.MethodGet,
    "https://example.com/download",
    nil,
)
if err != nil {
    return err
}

resp, err := client.Do(raw)
if err != nil {
    return err
}
defer resp.Body.Close()

_, err = io.Copy(dst, resp.Body)
```

`Client.Do` 不会应用 BaseURL、默认 Header 或任何 Builder 便利功能，也不会修改传入的 Request。需要这些便利功能时使用 `client.Get(...).Build()` 或 Builder 的 `Do()`。

Builder 的 `Do()` 直接返回原生响应：

```go
resp, err := client.Get(ctx, "/download").Do()
if err != nil {
    return err
}
defer resp.Body.Close()

_, err = io.Copy(dst, resp.Body)
```

`Build()` 只构造请求，不发送网络请求：

```go
httpReq, err := client.Post(ctx, "/users").
    JSON(payload).
    Build()
if err != nil {
    return err
}

// 可以继续使用标准库能力。
httpReq.Host = "internal.example.com"
resp, err := client.Do(httpReq)
```

## 响应读取

```go
err := client.Get(ctx, "/users/1").ToJSON(&user)

data, err := client.Get(ctx, "/raw").ToBytes()

text, err := client.Get(ctx, "/text").ToString()
```

三个 helper 的行为一致：

1. 发送请求。
2. 非 2xx 返回 `*StatusError`。
3. 读取 Body。
4. 关闭 Body。

成功响应默认最多读取 64 MiB，可通过选项调整：

```go
client := req.New(req.WithMaxResponseBytes(10 << 20))
```

超过上限时返回 `req.ErrResponseTooLarge`：

```go
if errors.Is(err, req.ErrResponseTooLarge) {
    // 使用 Do 配合 io.Copy 流式下载，而不是 ToBytes。
}
```

`Do` 不受该限制，也不会提前读取或关闭 Body。下载大文件应使用 `Do` + `io.Copy`。

空 Body（包括 `204 No Content`）传给 `ToJSON` 时返回 `nil`，不会修改目标值。JSON `null` 按 `encoding/json` 的标准语义处理。

## Debug

Debug 默认关闭。开启后默认写入 `os.Stdout`：

```go
client := req.New(
    req.WithDebug(true),
    req.WithBaseURL("https://api.example.com"),
)

var user User
err := client.Get(ctx, "/users/1").ToJSON(&user)
```

自定义输出目标、Body 捕获上限：

```go
client := req.New(
    req.WithDebug(true),
    req.WithDebugWriter(os.Stderr),
    req.WithDebugBody(true),             // 默认开启
    req.WithDebugMaxBodySize(64*1024),   // 默认 64 KiB
)
```

可以只为单次 Builder 请求开启或关闭 Debug：

```go
// Client 未开启 Debug，仅该请求输出。
err := client.Get(ctx, "/users").
    Debug(true).
    ToJSON(&users)

// Client 已开启 Debug，仅该请求静默。
err = client.Get(ctx, "/health").
    Debug(false).
    ToString()
```

Builder 的 Debug 覆盖会随 `Build()` 生成的请求一起保留，因此
`client.Do(builtRequest)` 也会遵守该设置。原生 `Client.Do` 在未携带 Builder
覆盖时使用 Client 级别的 Debug 配置。

输出包含：

- 请求 Method、完整 URL、Headers 和 Body。
- 响应 Status、Headers、Body 和耗时。
- 网络错误及其耗时。

安全与体积约束：

- `Authorization`、`Proxy-Authorization`、`Cookie`、`Set-Cookie` 以及名称中包含
  token、secret、password、api key、credential、signature 等标记的 Header 会输出为
  `[REDACTED]`。
- URL 中的 userinfo、fragment，以及 `token`、`access_token`、`api_key`、
  `secret`、`password`、`signature` 等常见敏感 query 参数会被脱敏。
- 只直接输出文本、JSON、XML、表单等可读 Body。二进制 Body 只显示 Content-Type
  和实际读取大小。
- 请求和响应 Body 分别最多捕获 64 KiB，超出部分标记为 truncated。该限制只影响
  Debug 输出，不截断真实 HTTP Body。
- 流式响应只记录调用方实际读取的字节，不会为了 Debug 提前读取或关闭 Body。
- Debug 使用独立包装器按需观察读写，不替换自定义 `http.Client`、
  `http.Transport`、`http.RoundTripper` 或 `DialContext`，也不影响连接复用。
- 同一个 Client 的 Debug 输出按块串行写入，避免并发请求日志交错。
- Debug 写入失败不会影响 HTTP 请求结果。最近一次写入错误可通过
  `client.DebugError()` 获取。

Body 可能包含任意敏感数据。开启 Debug 即表示允许记录可读 Body；处理密码、
令牌等敏感请求体时应显式关闭 Body 输出：

```go
client := req.New(
    req.WithDebug(true),
    req.WithDebugBody(false),
)
```

## 错误处理

错误分类包括：请求构造、URL 解析、JSON 编码/解码、网络、Context、超时、HTTP 状态和配置冲突。错误链使用 `%w` 保留。

HTTP 非 2xx：

```go
err := client.Get(ctx, "/missing").ToJSON(&result)

var statusErr *req.StatusError
if errors.As(err, &statusErr) {
    fmt.Println(statusErr.StatusCode) // 404
    fmt.Println(statusErr.Status)     // 404 Not Found
    fmt.Println(string(statusErr.Body))
}
```

`StatusError` 结构：

```go
type StatusError struct {
    StatusCode int
    Status     string
    Header     http.Header
    Body       []byte
}
```

错误响应的 Body 最多保留 64 KiB，避免恶意服务端造成内存问题。该截断不属于错误。

其他常用判断：

```go
errors.Is(err, req.ErrResponseTooLarge)
errors.Is(err, req.ErrNoBaseURL)
errors.Is(err, req.ErrConfiguration)
errors.Is(err, context.Canceled)
errors.Is(err, context.DeadlineExceeded)
```

HTTP 404/500 不会被伪装成网络错误；连接失败则会保留底层 `*url.Error` 链。

## Context 与 Timeout

所有请求都使用调用方传入的 Context：

```go
ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
defer cancel()

err := client.Get(ctx, "/users").ToJSON(&users)
```

`WithTimeout` 是 `http.Client.Timeout`，覆盖整个请求生命周期（连接、重定向、读取 Body）。使用外部 `WithHTTPClient` 时请直接设置其 `Timeout`。

## Body 生命周期

- `Do()`：不读取、不关闭响应 Body，调用方必须关闭。
- `ToJSON` / `ToBytes` / `ToString`：读取完成后自动关闭 Body；非 2xx 也会关闭。
- `JSON` / `Form`：调用时编码，支持多次 `Build`。
- `Body(reader)`：不缓冲、不复制、不关闭调用方 Reader，只消费一次。
- `Build()`：不发送请求。对 `Body(reader)` 连续 Build 两次，第二次从 Reader 当前位置继续读取，通常已经是 EOF。

```go
resp, err := client.Get(ctx, "/download").Do()
if err != nil {
    return err
}
defer resp.Body.Close()
```

调用方传入的 Reader 由调用方负责关闭：

```go
f, err := os.Open("payload.json")
if err != nil {
    return err
}
defer f.Close()

err = client.Post(ctx, "/upload").Body(f).ToJSON(&result)
```

## 并发安全

`Client` 在创建后配置不可变，可以安全地被多个 goroutine 共享：

```go
client := req.New(req.WithBaseURL("https://api.example.com"))

for i := 0; i < 10; i++ {
    go func(i int) {
        var user User
        _ = client.Get(context.Background(), "/users").
            Query("id", strconv.Itoa(i)).
            Header("X-ID", strconv.Itoa(i)).
            ToJSON(&user)
    }(i)
}
```

- 每次 `Get`/`Post`/`NewRequest` 都创建独立 Builder。
- Header、Query 和 JSON/Form Body 在不同请求之间不共享可变状态。
- 同一个 `Request` Builder 不要求支持多 goroutine 并发修改或并发执行。
- 通过 `HTTPClient()` 取得底层 Client 后，不要在其被并发使用时修改配置。
- 外部传入的 `http.Client` / `http.Transport` 由调用方负责并发安全，不要在请求期间修改。

## Options 语义与所有权

```go
type Option func(*Client) error
```

| Option | 行为 |
| --- | --- |
| `WithBaseURL` | 设置 BaseURL；重复设置时最后一个生效；必须是不含 query/fragment 的绝对 URL |
| `WithTimeout` | 设置 `req` 创建的 `http.Client.Timeout` |
| `WithHeader` | 累积默认 Header；同名 Header 最后一次生效 |
| `WithHTTPClient` | 直接使用外部 Client；不可与 Timeout/Transport/DialContext/代理 Option 组合 |
| `WithTransport` | 使用外部 RoundTripper；不修改它；不可与 HTTPClient/DialContext 组合；与代理组合时必须为 `*http.Transport` 并会被 Clone |
| `WithDialContext` | 克隆默认 Transport 并设置 DialContext；不可与 HTTPClient/Transport 组合；可与代理组合 |
| `WithProxy` | 设置固定代理 URL；校验 Scheme/Host/端口；不可与 HTTPClient 或自定义 RoundTripper 组合 |
| `WithoutProxy` | 设置 `Transport.Proxy = nil`，完全禁用环境变量代理 |
| `WithProxyFromEnvironment` | 恢复 `http.ProxyFromEnvironment`；这是默认行为 |
| `WithProxyFunc` | 使用标准库 Proxy 函数；由 Transport 按连接尝试调用 |
| `WithMaxResponseBytes` | 设置 ToJSON/ToBytes/ToString 的读取上限 |
| `WithDebug` | 开启或关闭 Debug；默认关闭 |
| `WithDebugWriter` | 设置 Debug 输出目标；默认 `os.Stdout` |
| `WithDebugBody` | 开启或关闭 Body 捕获；Debug 开启时默认开启 |
| `WithDebugMaxBodySize` | 设置单个请求/响应 Body 的 Debug 捕获上限；默认 64 KiB |

冲突检测与 Option 顺序无关。配置错误不会 panic：

```go
client := req.New(req.WithHTTPClient(hc), req.WithTimeout(time.Second))

// New 会保留错误，所有请求操作都会返回它。
if err := client.Err(); err != nil {
    // 处理配置错误。
}

// 需要立即处理配置错误时使用：
configured, err := req.NewWithOptions(
    req.WithHTTPClient(hc),
    req.WithTimeout(time.Second),
)
if err != nil {
    return err
}
client = configured
```

推荐运行时动态拼装配置时使用 `NewWithOptions`。

所有权规则：

- 默认 `http.Transport` 是 `http.DefaultTransport` 的独立 Clone。
- 默认 `http.Client` 归 `req` 所有。
- 外部 `http.Client` 和 `http.RoundTripper` 归调用方所有，`req` 不修改它们。
- `HTTPClient()` 返回底层指针；调用方不得在并发使用期间修改。
- `WithDialContext` 只修改自己克隆出来的 Transport。
- 代理 Option 只修改 `req` 自己克隆出来的 Transport；与 `WithTransport` 组合时不会修改调用方的 Transport。
- 代理凭据只用于 `http.Transport` 内部认证，不会写入 Debug 输出。

## URL 解析规则

`WithBaseURL("https://api.example.com/v1")` 时：

| 请求 URL | 最终 URL |
| --- | --- |
| `users` | `https://api.example.com/v1/users` |
| `/users` | `https://api.example.com/v1/users` |
| 空字符串 | `https://api.example.com/v1` |
| `/` | `https://api.example.com/v1/` |
| `../users` | `https://api.example.com/users` |
| `https://other.example.com/users` | 原样使用，忽略 BaseURL |
| `//other.example.com/users` | 继承 BaseURL 的 scheme |

- 相对路径无论是否以 `/` 开头，都追加到 BaseURL 的路径前缀。
- `.` / `..` 路径段会被清理。
- URL 编码使用 `net/url` 处理，不使用简单字符串拼接。
- 请求 URL 中的 query 会与 `Query`/`AddQuery`/`QueryValues` 合并。
- Fragment 会保留。
- 没有 BaseURL 时使用相对路径，会在 `Build`/`Do` 时返回 `ErrNoBaseURL`。

## 与 net/http 的设计关系

`go-req` 只增加请求构建便利性：

- 响应始终是标准库 `*http.Response`，没有自定义 Response 包装。
- 请求最终是标准库 `*http.Request`，可以通过 `Build()` 获取。
- 原生 `Client.Do` 路径完全不应用 BaseURL、默认 Header 等 Builder 特性。
- 不使用隐藏的全局状态，不修改 `http.DefaultClient` / `http.DefaultTransport`。
- 代理只是 `http.Transport.Proxy` 的一项配置，不是独立的网络实现层；不引入 ProxyManager、ProxyClient 等抽象。
- 不自动重试、不缓存响应、不自动读取成功响应 Body。
- 需要复杂网络配置时，直接使用 `http.Client` / `http.Transport` 或自定义 `http.RoundTripper`。

## 浏览器指纹扩展

浏览器 TLS/HTTP2 指纹模拟由独立模块 [`go-req-impersonate`](https://github.com/Xwudao/go-req-impersonate) 提供。核心库不依赖 uTLS、tls-client 或 fhttp，也不需要为此修改 `net/http` 兼容层。

```go
transport, err := impersonate.New(
    impersonate.Chrome(),
    impersonate.WithProxy("http://127.0.0.1:7890"),
)
if err != nil {
    return err
}

client := req.New(req.WithTransport(transport))
```

该模块返回标准 `http.RoundTripper`，因此仍然适用：

- `go-req` 的 Debug 可以正常包装该 Transport。
- `WithTransport` 不能与核心 `WithProxy` 或 `WithDialContext` 组合；冲突会返回 `ErrConfiguration`，不会静默忽略。
- 插件自身通过 `impersonate.WithProxy` 管理代理。
- 需要关闭空闲连接时调用 `transport.CloseIdleConnections()`。

指纹模拟不等于完整浏览器环境，也不保证绕过任何网站的检测。具体 Profile 版本、代理边界和限制请阅读该模块 README。

## 第一版暂不包含

自动重试、HTTP 缓存、WebSocket、SSE 专用抽象、Cookie 持久化封装、自动限流、熔断器、复杂 Middleware、服务发现、泛型 DSL、内置日志框架。

这些能力可以通过 `http.RoundTripper`、`http.Client` 或独立模块组合实现。

## 测试

```bash
go test ./...
go test -race ./...
go vet ./...
```

测试仅使用标准库 `testing` 与 `httptest`，不依赖公网服务。
