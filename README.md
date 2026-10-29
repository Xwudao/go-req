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

要求 Go 1.27 或更高版本（JSON 解码使用 Go 1.27 引入的泛型方法）。

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

user, err := client.Get(ctx, "https://api.example.com/users/1").ToJSON[User]()
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
user, err := client.Get(ctx, "/users/1").ToJSON[User]()
if err != nil {
    return err
}
```

POST JSON 不需要手动 `json.Marshal`：

```go
result, err := client.Post(ctx, "/users").
    JSON(map[string]any{
        "name": "Alice",
        "age":  25,
    }).
    ToJSON[CreateUserResponse]()
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
users, err := client.Get(ctx, "/users").
    Query("page", "1").
    Query("limit", "20").
    ToJSON[[]User]()
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

Client 级默认 Query（如 `appid`、`access_token`）：

```go
client := req.New(
    req.WithBaseURL("https://api.example.com"),
    req.WithQuery("appid", "wx123"),
)

client.Get(ctx, "/users")               // ?appid=wx123
client.Get(ctx, "/users?appid=other")   // URL 中已有同名参数时不添加默认值
client.Get(ctx, "/users").Query("appid", "x") // Builder 上的操作在默认值之后生效
```

## 路径参数

```go
client.Get(ctx, "/users/{id}/posts/{pid}").
    PathParam("id", "42").
    PathParam("pid", "7")

// 也可以一次传入 map
client.Get(ctx, "/users/{id}/posts/{pid}").
    PathParams(map[string]string{"id": "42", "pid": "7"})
```

- 值使用 `url.PathEscape` 转义，`"a/b c"` 会变成 `a%2Fb%20c`，不会多出路径段。
- 值不能为空，也不能是 `.` 或 `..`，避免意外请求到别的接口。
- 只要设置了路径参数，URL 路径中的每个 `{name}` 都必须有值，每个参数也必须
  对应一个占位符，否则 `Build` 返回错误。只替换路径部分，query 和 fragment 不受影响。

## Header、Cookie 与认证

```go
result, err := client.Get(ctx, "/users").
    Header("Authorization", "Bearer token").
    Header("X-Request-ID", "123").
    ToJSON[Result]()
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

### 请求钩子：签名与动态 Token

`WithBeforeRequest` 在请求构建完成（URL、Query、Header、Body 都已设置）后、
发送前执行，可以修改请求，适合签名或注入会过期的 token：

```go
client := req.New(
    req.WithBaseURL("https://api.example.com"),
    req.WithBeforeRequest(func(r *http.Request) error {
        ts := strconv.FormatInt(time.Now().Unix(), 10)
        var body []byte
        if r.GetBody != nil { // JSON/Form Body 可通过 GetBody 读取副本，不影响发送
            rc, err := r.GetBody()
            if err != nil {
                return err
            }
            body, _ = io.ReadAll(rc)
        }
        r.Header.Set("X-Timestamp", ts)
        r.Header.Set("X-Sign", sign(r.Method, r.URL.RequestURI(), ts, body))
        return nil
    }),
    req.WithBeforeRequest(func(r *http.Request) error {
        token, err := tokens.Get(r.Context()) // 自己的 token 缓存/刷新逻辑
        if err != nil {
            return err
        }
        r.Header.Set("Authorization", "Bearer "+token)
        return nil
    }),
)

// 单个请求追加钩子，在 Client 的钩子之后执行。
client.Get(ctx, "/x").BeforeRequest(func(r *http.Request) error { ... })
```

- 多个钩子按添加顺序执行：先 Client 的，再 Request 的。
- 钩子返回错误时请求不会发送。
- 钩子在 `Build` 中执行，所以 `Build` 后再 `client.Do` 也已经签过名；直接传给
  `Client.Do` 的原生请求不会执行钩子。
- `Body(reader)` 设置的 Body 没有 `GetBody`，不要在钩子里读取 `r.Body`。

## Form 请求

```go
result, err := client.Post(ctx, "/login").
    Form(url.Values{
        "username": {"Alice"},
        "password": {"secret"},
    }).
    ToJSON[LoginResult]()
```

Form 使用 `url.Values.Encode()`，Content-Type 默认为 `application/x-www-form-urlencoded`。

## Multipart 文件上传

```go
file, err := os.Open("report.pdf")
if err != nil {
    return err
}
defer file.Close() // Reader 始终归调用方所有

result, err := client.Post(ctx, "/upload").Multipart(
    url.Values{"description": {"月报"}},
    req.MultipartFile{
        FieldName:   "files",
        FileName:    "report.pdf",
        ContentType: "application/pdf", // 可省略，默认 application/octet-stream
        Reader:      file,
    },
).ToJSON[UploadResult]()
```

可传多个 `MultipartFile`，允许相同 FieldName。字段和文件元数据在调用时复制；文件不缓冲到内存，Build 不读取文件，也不启动后台 goroutine。文件 Reader 只消费一次，不支持自动重放或重定向重发；重复 Build 不会倒回。调用方负责关闭文件，包括请求失败或取消时。Context 在每次读取前检查；阻塞中的自定义 Reader 需要调用方负责解除阻塞。

Multipart 自动生成带 boundary 的 Content-Type。显式 Header 会覆盖它，通常不应手动设置。无论使用 Body、JSON、Form 还是 Multipart，切换 Body 类型时都应确保 Content-Type 与实际内容匹配。

## 重定向

默认和标准库一样自动跟随重定向（最多 10 次）。只想拿到跳转地址时用 `ToLocation`：

```go
loc, err := client.Get(ctx, "/short/abc").ToLocation()
if err != nil {
    return err
}
fmt.Println(loc.String()) // 相对 Location 已按请求 URL 解析成绝对地址
```

- 不会跟随跳转，只发送一次请求；Body 自动丢弃并关闭。
- 3xx 但没有 `Location` Header 时，错误满足 `errors.Is(err, http.ErrNoLocation)`。
- 非 3xx 返回 `*ResponseError`（非 2xx 会执行 ErrorCheck）。

需要 3xx 响应的其他信息（Header、Set-Cookie 等）时，用 `NoRedirect`：

```go
resp, err := client.Get(ctx, "/login").NoRedirect().Do()
// resp.StatusCode == 302，resp.Header.Get("Location")、resp.Cookies() 均可读取

_, info, err := client.Post(ctx, "/login").Form(form).NoRedirect().ToJSONResponse[any]()
// 3xx 按非 2xx 处理，err 为 *ResponseError，info.Header 仍可读取
```

Client 级别的策略：

```go
req.New(req.WithoutRedirect()) // 所有请求都不跟随

req.New(req.WithCheckRedirect(func(r *http.Request, via []*http.Request) error {
    if len(via) >= 3 {
        return errors.New("too many redirects")
    }
    return nil
})) // 与 http.Client.CheckRedirect 含义相同
```

- `NoRedirect` 只影响当前请求：使用底层 `http.Client` 的浅拷贝，不修改共享的
  Client、Transport 和 CookieJar，因此与 `WithHTTPClient` 一起用也没问题。
  与 Debug 一样，它随 `Build()` 生成的请求保留，`Build` 后再 `client.Do` 同样生效。
- `WithoutRedirect` / `WithCheckRedirect` 不能与 `WithHTTPClient` 组合，请直接设置
  外部 Client 的 `CheckRedirect`。两者同时使用时最后一个生效。

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
user, err := client.Get(ctx, "/users/1").ToJSON[User]()

data, err := client.Get(ctx, "/raw").ToBytes()

text, err := client.Get(ctx, "/text").ToString()
```

三个 helper 的行为一致：

1. 发送请求。
2. 读取 Body（非 2xx 最多读取 64 KiB）并关闭。
3. 执行 ErrorCheck（如果配置了），由它判定是否为 API 错误。
4. 非 2xx 或被判定为错误时返回 `*ResponseError`。

`ToJSON[T]` 中 `T` 是任意可被 `encoding/json` 解码的类型：

```go
user, err := client.Get(ctx, "/users/1").ToJSON[User]()
users, err := client.Get(ctx, "/users").ToJSON[[]User]()
raw, err := client.Get(ctx, "/anything").ToJSON[map[string]any]()
```

空 Body（包括 `204 No Content`）返回 `T` 的零值且没有错误。需要区分
“空响应”和“解码出的零值”时，用指针类型：

```go
user, err := client.Get(ctx, "/users/1").ToJSON[*User]()
// 204 No Content 或 JSON null 时 user == nil，err == nil。
```

需要成功响应的 Header 或状态码（分页、限流信息等）时，使用 `ToJSONResponse`：

```go
users, resp, err := client.Get(ctx, "/users").ToJSONResponse[[]User]()
if err != nil {
    return err
}
total := resp.Header.Get("X-Total-Count")
```

只要收到了响应，`resp` 就不为 nil：非 2xx、ErrorCheck 判定的错误、JSON 解码失败
时都能读取 Header（例如 429 的 `Retry-After`）。构建、网络和读取错误时为 nil。

成功响应默认最多读取 64 MiB，可通过选项调整：

```go
client := req.New(req.WithMaxResponseBytes(10 << 20))
```

超过上限时返回 `req.ErrResponseTooLarge`：

```go
if errors.Is(err, req.ErrResponseTooLarge) {
    // 使用 ToWriter 或 Do 流式下载，而不是 ToBytes。
}
```

单次请求可以覆盖读取上限，不影响同一个 Client 的其他请求：

```go
data, err := client.Get(ctx, "/raw").MaxResponseBytes(1 << 20).ToBytes()
```

上限必须大于零且小于 `math.MaxInt64`，仅影响 ToJSON/ToBytes/ToString，不影响 Do/ToWriter。

`Do` 不受该限制，也不会提前读取或关闭 Body。下载大文件可使用 `Do` + `io.Copy`，或使用 `ToWriter`：

```go
file, err := os.Create("report.zip")
if err != nil {
    return err
}
defer file.Close()
n, err := client.Get(ctx, "/report.zip").ToWriter(file)
```

`ToWriter` 检查 2xx 状态、流式写入、自动关闭响应 Body，但不关闭 Writer；失败时返回已写入的字节数。非 2xx 不写入 Writer，返回 `*ResponseError`。2xx Body 是流式写入而非缓冲读取，因此 ErrorCheck 只对非 2xx 生效。它没有隐式大小上限，处理不可信下载时请配置 Context deadline 或限量 Writer。发生错误时 Writer 可能已有部分数据；文件创建、覆盖、清理由调用方负责。

JSON 解码可按请求启用选项，默认行为不变：

```go
data, err := client.Get(ctx, "/numbers").UseNumber(true).ToJSON[map[string]any]()
// data 中的数字使用 json.Number，避免 float64 精度损失。

user, err := client.Get(ctx, "/user").DisallowUnknownFields(true).ToJSON[User]()
// 返回结构体未定义的字段时解码失败。
```

两项可组合，仍只接受单个完整 JSON 文档，不接受尾随内容。

### 流式响应：SSE 与 JSON Lines

`ToSSE` 逐个返回 Server-Sent Events，`ToJSONStream[T]` 逐行解码 NDJSON / JSON
Lines，都使用 Go 的 range-over-func 迭代器：

```go
for ev, err := range client.Post(ctx, "/chat").JSON(body).ToSSE() {
    if err != nil {
        return err
    }
    if ev.Data == "[DONE]" { // 结束标记由你判断，库不做假设
        break
    }
    chunk, err := ev.JSON[Chunk]()
    if err != nil {
        return err
    }
    fmt.Print(chunk.Text)
}

for entry, err := range client.Get(ctx, "/logs").ToJSONStream[LogEntry]() {
    if err != nil {
        return err
    }
    fmt.Println(entry.Message)
}
```

`SSEEvent` 包含 `ID`、`Event`、`Data`（多行 data 用 `\n` 连接）和 `Retry`。

语义：

- 每次 `range` 都会发送一次请求；循环结束（读完、`break` 或出错）时自动关闭 Body。
- 产生错误后迭代立即结束。
- 非 2xx 只产生一次 `*ResponseError`（会执行 ErrorCheck）。
- ErrorCheck 对**每个事件的 data / 每一行**执行。流中途推送的错误对象（如
  `{"code":5,"msg":"busy"}`）会像普通响应一样被识别，`errors.As` 可直接取到。
- `ToSSE` 收到 2xx 但 Content-Type 是 JSON 时（有些 API 出错时不建立流），会整体读取
  并执行 ErrorCheck；未被判定为错误时返回 “expected an event stream” 错误。
- 单行和单个事件的 data 受 `WithMaxResponseBytes` / `MaxResponseBytes` 限制，超过时
  返回 `ErrResponseTooLarge`。整个流没有总大小限制，需要时配合 `Timeout` 或 Context。
- SSE 解析遵循规范：支持 `\r\n`、`\n`、`\r` 换行，忽略注释行和开头的 BOM，
  `id` 在后续事件中保留。流末尾缺少空行时，最后一个事件仍会返回。
- `ToJSONStream` 跳过空行，解码遵循 `UseNumber` 和 `DisallowUnknownFields`。

### API 错误：ErrorCheck

很多 API 的成功响应和错误响应是完全不同的 JSON，而且错误不一定用 HTTP
状态表示：

```text
HTTP 200 {"id":7,"name":"alice"}              成功
HTTP 200 {"code":40001,"msg":"token expired"} 失败
HTTP 401 {"code":40100,"msg":"unauthorized"}  失败
```

什么样的响应算错误，完全由你决定：在 Client 上配置一次 `ErrorCheck`，
所有请求的调用处保持 `ToJSON[T]()` 不变。

```go
type APIError struct {
    Code int    `json:"code"`
    Msg  string `json:"msg"`
}

func (e APIError) Error() string { return fmt.Sprintf("%d: %s", e.Code, e.Msg) }

client := req.New(
    req.WithBaseURL("https://api.example.com"),
    req.WithErrorCheck(req.JSONError(func(resp *req.Response, e APIError) bool {
        return e.Code != 0 // 判定规则由你决定，可以结合 resp.StatusCode / resp.Header
    })),
)

user, err := client.Get(ctx, "/users/1").ToJSON[User]()

var apiErr APIError
if errors.As(err, &apiErr) {
    // 不管是 HTTP 200 还是 4xx/5xx，只要判定为 API 错误，都在这里处理。
    fmt.Println(apiErr.Code, apiErr.Msg)
}
```

`JSONError[E]` 的规则：

- 对每个非空 Body，尝试解码为 `E`；解码失败（例如成功响应是数组、错误页是
  HTML）或 Body 为 JSON `null` 时不算 API 错误，交给默认的状态码处理。
- 解码成功后调用你的判定函数；返回 `true` 时 `E` 作为错误返回。
- `E` 必须实现 `error`，通常用值类型。解码 `E` 时忽略 `DisallowUnknownFields`
  和 `UseNumber`（成功 Body 通常含有 `E` 未声明的字段）。
- 判定函数接收 `*req.Response`，可以按状态码、Header 等区分，例如
  `return !resp.IsSuccess()` 只把非 2xx 当作 API 错误。

需要完全自定义（非 JSON、按 Header 判定、返回自己的错误类型等）时，直接写
`ErrorCheck`：

```go
type ErrorCheck func(resp *req.Response) error

client := req.New(req.WithErrorCheck(func(resp *req.Response) error {
    if resp.Header.Get("X-Error-Code") != "" {
        return fmt.Errorf("upstream error %s", resp.Header.Get("X-Error-Code"))
    }
    return nil
}))
```

返回非 nil 即判定为错误，2xx 也一样；返回 nil 则保持默认行为：2xx 成功，
非 2xx 返回不带 `Err` 的 `*ResponseError`。ErrorCheck 不能把非 2xx 变成成功。

单个请求可以覆盖或关闭 Client 的 ErrorCheck：

```go
// 这个接口属于另一个服务，错误格式不同。
data, err := client.Get(ctx, "https://other.example.com/x").
    ErrorCheck(otherCheck).
    ToJSON[Data]()

// 传 nil 关闭检查，只按 HTTP 状态判断。
data, err := client.Get(ctx, "/raw").ErrorCheck(nil).ToJSON[Data]()
```

ErrorCheck 作用于 `ToJSON`、`ToBytes`、`ToString` 以及 `ToWriter` 的非 2xx
响应。原生 `Do` 不读取 Body，因此不会执行检查。

如果成功和失败共用一个外层结构（如 `{"code":0,"data":{...}}`），可以直接
定义该结构，再配合 ErrorCheck 判定 code：

```go
type Envelope[T any] struct {
    Code int    `json:"code"`
    Msg  string `json:"msg"`
    Data T      `json:"data"`
}

env, err := client.Get(ctx, "/users/1").ToJSON[Envelope[User]]()
user := env.Data
```

## Debug

Debug 默认关闭。开启后默认写入 `os.Stdout`：

```go
client := req.New(
    req.WithDebug(true),
    req.WithBaseURL("https://api.example.com"),
)

user, err := client.Get(ctx, "/users/1").ToJSON[User]()
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
users, err := client.Get(ctx, "/users").
    Debug(true).
    ToJSON[[]User]()

// Client 已开启 Debug，仅该请求静默。
text, err := client.Get(ctx, "/health").
    Debug(false).
    ToString()
```

Builder 的 Debug 覆盖会随 `Build()` 生成的请求一起保留，因此
`client.Do(builtRequest)` 也会遵守该设置。原生 `Client.Do` 在未携带 Builder
覆盖时使用 Client 级别的 Debug 配置。

并发请求的 Request 和 Response 日志包含相同的进程内 `Request-ID`，不会将该 ID 注入实际 HTTP Header。`Headers-Duration` 表示发送开始到收到响应头的耗时（包括重定向）；`Body-Duration` 表示返回响应头后到 Body EOF/Close 的耗时，包含调用方暂停读取的时间；`Duration` 为总耗时。Body 未读取或关闭时，不会提前输出其完成日志。

业务自定义脱敏规则（重复配置累积，名称大小写不敏感，内置规则仍生效）：

```go
client := req.New(
    req.WithDebug(true),
    req.WithDebugRedactHeaders("X-Business-Identity"),
    req.WithDebugRedactQuery("customer_id"),
    req.WithDebugRedactJSONFields("phone", "address"),
)
```

JSON 字段在任意嵌套深度匹配；启用 JSON 字段脱敏后，截断、不完整或无效 JSON 会整段省略，避免部分内容泄露。规则仅作用于日志副本，不改变真实请求/响应。JSON 字段规则不作用于非 JSON Body，敏感表单等内容仍应关闭 Body 日志。

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

响应级错误（非 2xx，或 ErrorCheck 判定的 API 错误）统一返回 `*ResponseError`：

```go
user, err := client.Get(ctx, "/missing").ToJSON[User]()

var respErr *req.ResponseError
if errors.As(err, &respErr) {
    fmt.Println(respErr.StatusCode) // 404
    fmt.Println(respErr.Status)     // 404 Not Found
    fmt.Println(string(respErr.Body))
}
```

结构：

```go
type Response struct {
    StatusCode    int
    Status        string
    Header        http.Header
    Body          []byte
    BodyTruncated bool // 仅非 2xx：Body 是否超过 64 KiB 被截断
}

type ResponseError struct {
    Response
    Err error // ErrorCheck 返回的错误；普通非 2xx 时为 nil
}
```

`ResponseError.Unwrap` 返回 `Err`，所以 `errors.As(err, &apiErr)` 能直接拿到
ErrorCheck 返回的 API 错误类型，同时 `errors.As(err, &respErr)` 仍可取得状态码和
原始 Body。

非 2xx 的 Body 最多保留 64 KiB，避免恶意服务端造成内存问题。超过时 BodyTruncated 为 true；恰好等于上限为 false。该截断不属于错误。如果读取本身失败，错误链同时保留 `*ResponseError` 和底层读取错误，且不会执行 ErrorCheck；BodyTruncated 为 false 不代表读取一定完成，应同时检查错误链。

其他常用判断：

```go
errors.Is(err, req.ErrResponseTooLarge)
errors.Is(err, req.ErrNoBaseURL)
errors.Is(err, req.ErrConfiguration)
errors.Is(err, context.Canceled)
errors.Is(err, context.DeadlineExceeded)
```

`WithTimeout` 底层是 `http.Client.Timeout`，超时错误满足
`errors.Is(err, context.DeadlineExceeded)`。

HTTP 404/500 不会被伪装成网络错误；连接失败则会保留底层 `*url.Error` 链。

## Context 与 Timeout

所有请求都使用调用方传入的 Context：

```go
ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
defer cancel()

users, err := client.Get(ctx, "/users").ToJSON[[]User]()
```

`WithTimeout` 是 `http.Client.Timeout`，覆盖整个请求生命周期（连接、重定向、读取 Body）。使用外部 `WithHTTPClient` 时请直接设置其 `Timeout`。

单个请求的超时用 `Timeout`，不需要自己创建 Context：

```go
user, err := client.Get(ctx, "/slow").Timeout(3 * time.Second).ToJSON[User]()
```

- 同样覆盖读取 Body 的时间，超时错误满足 `errors.Is(err, context.DeadlineExceeded)`。
- 与 `WithTimeout`、传入的 Context 同时生效，以最先到期的为准。
- 在发送时生效（`Do`、`ToJSON`、`ToSSE` 等）；`Build` 返回的请求不带该超时。
  使用 `Do` 时，关闭响应 Body 会释放计时器。

## Body 生命周期

- `Do()`：不读取、不关闭响应 Body，调用方必须关闭。
- `ToJSON` / `ToJSONResponse` / `ToBytes` / `ToString`：读取完成后自动关闭 Body；非 2xx 也会关闭。
- `ToSSE` / `ToJSONStream`：循环结束（包括 `break`）时自动关闭 Body。
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

result, err := client.Post(ctx, "/upload").Body(f).ToJSON[Result]()
```

## 并发安全

`Client` 在创建后配置不可变，可以安全地被多个 goroutine 共享：

```go
client := req.New(req.WithBaseURL("https://api.example.com"))

for i := 0; i < 10; i++ {
    go func(i int) {
        _, _ = client.Get(context.Background(), "/users").
            Query("id", strconv.Itoa(i)).
            Header("X-ID", strconv.Itoa(i)).
            ToJSON[User]()
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
| `WithoutRedirect` | 不跟随重定向，直接返回 3xx 响应；不可与 HTTPClient 组合 |
| `WithCheckRedirect` | 设置 `http.Client.CheckRedirect`；不可与 HTTPClient 组合；与 WithoutRedirect 最后一个生效 |
| `WithQuery` | 设置默认 Query 参数；URL 中已有同名参数时不添加；同名最后一次生效 |
| `WithBeforeRequest` | 累积请求钩子，按添加顺序在 Build 时执行；不能为 nil |
| `WithErrorCheck` | 设置判定 API 错误的 ErrorCheck；不能为 nil；可被 `Request.ErrorCheck` 覆盖 |
| `WithDebug` | 开启或关闭 Debug；默认关闭 |
| `WithDebugWriter` | 设置 Debug 输出目标；默认 `os.Stdout` |
| `WithDebugBody` | 开启或关闭 Body 捕获；Debug 开启时默认开启 |
| `WithDebugMaxBodySize` | 设置单个请求/响应 Body 的 Debug 捕获上限；默认 64 KiB |
| `WithDebugRedactHeaders` | 累积业务 Header 脱敏名称，大小写不敏感 |
| `WithDebugRedactQuery` | 累积业务 Query 脱敏名称，大小写不敏感 |
| `WithDebugRedactJSONFields` | 累积 JSON 字段脱敏名称；匹配任意嵌套深度，不完整 JSON 整段省略 |

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

## BaseURL 与认证安全

BaseURL 只是 URL 解析前缀，**不是域名白名单或 SSRF 防护**。绝对 URL 和 `//host/path` 可以切换目标主机，Builder 默认 Header（包括 Authorization）也会应用到该目标。不要把不可信 URL 直接交给带默认凭据的 Client；必要时在 Build 后校验 scheme、主机、端口，再发送。

标准库的重定向处理不等同于严格同源策略；高安全场景应自行配置 `http.Client.CheckRedirect`，检查每一跳。本文新增功能不会引入隐式同源限制或改变标准库行为。

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

浏览器 TLS/HTTP2 指纹模拟由独立模块 [`go-req-ext/impersonate`](https://github.com/Xwudao/go-req-ext/tree/main/impersonate) 提供：

```bash
go get github.com/Xwudao/go-req-ext/impersonate
```

核心库不依赖 uTLS、tls-client 或 fhttp，也不需要为此修改 `net/http` 兼容层。

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

Cookie 管理与持久化由同一扩展仓库的 [`go-req-ext/jar`](https://github.com/Xwudao/go-req-ext/tree/main/jar) 提供，只依赖标准库；使用 `jar` 不会链接指纹模块。

## 第一版暂不包含

自动重试、HTTP 缓存、WebSocket、SSE 专用抽象、Cookie 持久化封装、自动限流、熔断器、复杂 Middleware、服务发现、复杂泛型 DSL、内置日志框架。

这些能力可以通过 `http.RoundTripper`、`http.Client` 或独立模块组合实现。

## 测试

```bash
go test ./...
go test -race ./...
go vet ./...
go test -run '^$' -bench . -benchmem ./...
go test -run '^$' -fuzz '^FuzzResolveURL$' -fuzztime 10s
go test -run '^$' -fuzz '^FuzzProxyConfiguration$' -fuzztime 10s
go test -run '^$' -fuzz '^FuzzDebugRedaction$' -fuzztime 10s
```

测试仅使用标准库 `testing` 与 `httptest`，不依赖公网服务。CI 在 Go 1.27 和当前稳定版执行测试、race 检测和 vet，并在稳定版运行短时间 Fuzz。Benchmark 对比原生请求构造与 Builder，并测量 JSON 和 Debug 的分配开销；响应 Benchmark 使用本地 RoundTripper，不代表真实网络性能。
