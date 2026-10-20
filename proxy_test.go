package req_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Xwudao/go-req"
)

// testProxy is a small HTTP forward proxy used only by tests. It supports
// absolute-form HTTP requests, HTTPS CONNECT tunnels, and optional Basic proxy
// authentication. It deliberately implements no other proxy protocol.
type testProxy struct {
	server   *httptest.Server
	username string
	password string
	forward  *http.Transport

	mu            sync.Mutex
	httpRequests  []string
	connectTarget []string
	authHeaders   []string
}

func newTestProxy(t *testing.T, username, password string) *testProxy {
	t.Helper()

	p := &testProxy{
		username: username,
		password: password,
		forward:  &http.Transport{Proxy: nil},
	}
	p.server = httptest.NewServer(http.HandlerFunc(p.handle))
	t.Cleanup(func() {
		p.forward.CloseIdleConnections()
		p.server.Close()
	})
	return p
}

func (p *testProxy) handle(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Proxy-Authorization")
	p.mu.Lock()
	p.authHeaders = append(p.authHeaders, auth)
	p.mu.Unlock()

	if p.username != "" || p.password != "" {
		expected := "Basic " + base64.StdEncoding.EncodeToString(
			[]byte(p.username+":"+p.password),
		)
		if auth != expected {
			w.Header().Set("Proxy-Authenticate", `Basic realm="test-proxy"`)
			http.Error(w, "proxy authentication required", http.StatusProxyAuthRequired)
			return
		}
	}

	if r.Method == http.MethodConnect {
		p.mu.Lock()
		p.connectTarget = append(p.connectTarget, r.Host)
		p.mu.Unlock()
		p.handleConnect(w, r)
		return
	}

	if !r.URL.IsAbs() {
		http.Error(w, "proxy requires absolute request URL", http.StatusBadRequest)
		return
	}

	p.mu.Lock()
	p.httpRequests = append(p.httpRequests, r.URL.String())
	p.mu.Unlock()

	out, err := http.NewRequestWithContext(r.Context(), r.Method, r.URL.String(), r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	copyHeaders(out.Header, r.Header)
	out.Header.Del("Proxy-Authorization")
	out.Header.Del("Proxy-Connection")

	resp, err := p.forward.RoundTrip(out)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (p *testProxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	destination, err := net.DialTimeout("tcp", r.Host, 5*time.Second)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer destination.Close()

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking is not supported", http.StatusInternalServerError)
		return
	}
	client, _, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer client.Close()

	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}

	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(destination, client)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(client, destination)
		done <- struct{}{}
	}()
	<-done
}

func (p *testProxy) httpRequestCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.httpRequests)
}

func (p *testProxy) connectCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.connectTarget)
}

func (p *testProxy) lastConnectTarget() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.connectTarget) == 0 {
		return ""
	}
	return p.connectTarget[len(p.connectTarget)-1]
}

func (p *testProxy) lastAuthHeader() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.authHeaders) == 0 {
		return ""
	}
	return p.authHeaders[len(p.authHeaders)-1]
}

func copyHeaders(dst, src http.Header) {
	for key, values := range src {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func proxyURLWithCredentials(t *testing.T, raw, username, password string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse proxy URL: %v", err)
	}
	u.User = url.UserPassword(username, password)
	return u.String()
}

func newTLSProxyClient(t *testing.T, proxyURL string, transport *http.Transport) *req.Client {
	t.Helper()
	opts := []req.Option{
		req.WithTransport(transport),
		req.WithProxy(proxyURL),
	}
	client, err := req.NewWithOptions(opts...)
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}
	t.Cleanup(client.CloseIdleConnections)
	return client
}

func TestWithProxyHTTP(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Proxy-Authorization"); got != "" {
			t.Errorf("target received Proxy-Authorization %q", got)
		}
		_, _ = io.WriteString(w, "through-http-proxy")
	}))
	defer target.Close()

	proxy := newTestProxy(t, "", "")
	client := req.New(req.WithProxy(proxy.server.URL))
	t.Cleanup(client.CloseIdleConnections)

	body, err := client.Get(context.Background(), target.URL+"/hello").ToString()
	if err != nil {
		t.Fatalf("ToString: %v", err)
	}
	if body != "through-http-proxy" {
		t.Fatalf("body = %q", body)
	}
	if proxy.httpRequestCount() != 1 {
		t.Fatalf("proxy saw %d HTTP requests, want 1", proxy.httpRequestCount())
	}
}

func TestWithProxyHTTPForwardsRequest(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "forwarded:"+r.URL.Path)
	}))
	defer target.Close()

	proxy := newTestProxy(t, "", "")
	client, err := req.NewWithOptions(req.WithProxy(proxy.server.URL))
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}
	t.Cleanup(client.CloseIdleConnections)

	body, err := client.Get(context.Background(), target.URL+"/hello").ToString()
	if err != nil {
		t.Fatalf("ToString: %v", err)
	}
	if body != "forwarded:/hello" {
		t.Fatalf("body = %q", body)
	}
	if proxy.httpRequestCount() != 1 {
		t.Fatalf("proxy saw %d HTTP requests, want 1", proxy.httpRequestCount())
	}
}

func TestWithProxyHTTPSConnectPreservesTransport(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "through-connect")
	}))
	defer target.Close()

	proxy := newTestProxy(t, "", "")

	original := http.DefaultTransport.(*http.Transport).Clone()
	original.Proxy = nil
	original.MaxIdleConns = 17
	original.MaxIdleConnsPerHost = 5
	original.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // test server certificate
	originalDial := func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	original.DialContext = originalDial

	client := newTLSProxyClient(t, proxy.server.URL, original)
	transport, ok := client.HTTPClient().Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T", client.HTTPClient().Transport)
	}
	if transport == original {
		t.Fatal("caller-supplied transport was reused instead of cloned")
	}
	if original.Proxy != nil {
		t.Fatal("caller-supplied transport Proxy was modified")
	}
	if original.MaxIdleConns != 17 || original.MaxIdleConnsPerHost != 5 {
		t.Fatalf("caller transport pool settings changed: %d, %d", original.MaxIdleConns, original.MaxIdleConnsPerHost)
	}
	if transport.MaxIdleConns != 17 || transport.MaxIdleConnsPerHost != 5 {
		t.Fatalf("clone lost pool settings: %d, %d", transport.MaxIdleConns, transport.MaxIdleConnsPerHost)
	}
	if transport.TLSClientConfig == nil || !transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("clone lost TLSClientConfig")
	}
	if reflect.ValueOf(transport.DialContext).Pointer() != reflect.ValueOf(originalDial).Pointer() {
		t.Fatal("clone lost DialContext")
	}
	if transport.Proxy == nil {
		t.Fatal("clone has no proxy function")
	}

	body, err := client.Get(context.Background(), target.URL).ToString()
	if err != nil {
		t.Fatalf("ToString: %v", err)
	}
	if body != "through-connect" {
		t.Fatalf("body = %q", body)
	}
	if proxy.connectCount() != 1 {
		t.Fatalf("proxy saw %d CONNECT requests, want 1", proxy.connectCount())
	}
	if got := proxy.lastConnectTarget(); got == "" {
		t.Fatal("proxy did not record CONNECT target")
	}
}

func TestWithProxyAuthentication(t *testing.T) {
	const (
		username = "proxy-user"
		password = "proxy-secret"
	)

	t.Run("http", func(t *testing.T) {
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "authenticated-http")
		}))
		defer target.Close()

		proxy := newTestProxy(t, username, password)
		client, err := req.NewWithOptions(req.WithProxy(
			proxyURLWithCredentials(t, proxy.server.URL, username, password),
		))
		if err != nil {
			t.Fatalf("NewWithOptions: %v", err)
		}
		t.Cleanup(client.CloseIdleConnections)

		body, err := client.Get(context.Background(), target.URL).ToString()
		if err != nil {
			t.Fatalf("ToString: %v", err)
		}
		if body != "authenticated-http" {
			t.Fatalf("body = %q", body)
		}
		expected := "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
		if got := proxy.lastAuthHeader(); got != expected {
			t.Fatalf("Proxy-Authorization = %q, want %q", got, expected)
		}
	})

	t.Run("https connect", func(t *testing.T) {
		target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "authenticated-connect")
		}))
		defer target.Close()

		proxy := newTestProxy(t, username, password)
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		client := newTLSProxyClient(
			t,
			proxyURLWithCredentials(t, proxy.server.URL, username, password),
			transport,
		)

		body, err := client.Get(context.Background(), target.URL).ToString()
		if err != nil {
			t.Fatalf("ToString: %v", err)
		}
		if body != "authenticated-connect" {
			t.Fatalf("body = %q", body)
		}
		if proxy.connectCount() != 1 {
			t.Fatalf("proxy saw %d CONNECT requests, want 1", proxy.connectCount())
		}
	})
}

func TestWithoutProxyConnectsDirectly(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "direct")
	}))
	defer target.Close()

	// This address must never be dialed when the proxy environment is ignored.
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("NO_PROXY", "")

	client, err := req.NewWithOptions(req.WithoutProxy())
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}
	t.Cleanup(client.CloseIdleConnections)
	transport, ok := client.HTTPClient().Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T", client.HTTPClient().Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("WithoutProxy left a non-nil Proxy function")
	}

	body, err := client.Get(context.Background(), target.URL).ToString()
	if err != nil {
		t.Fatalf("ToString: %v", err)
	}
	if body != "direct" {
		t.Fatalf("body = %q", body)
	}
}

func TestWithProxyFromEnvironment(t *testing.T) {
	client, err := req.NewWithOptions(
		req.WithoutProxy(),
		req.WithProxyFromEnvironment(),
	)
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}
	transport, ok := client.HTTPClient().Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T", client.HTTPClient().Transport)
	}
	if transport.Proxy == nil {
		t.Fatal("WithProxyFromEnvironment left Proxy nil")
	}
	if reflect.ValueOf(transport.Proxy).Pointer() != reflect.ValueOf(http.ProxyFromEnvironment).Pointer() {
		t.Fatal("Proxy is not http.ProxyFromEnvironment")
	}

	// Proxy selection is last-one-wins, independent of the order of other
	// transport options.
	disabled, err := req.NewWithOptions(
		req.WithProxyFromEnvironment(),
		req.WithoutProxy(),
	)
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}
	disabledTransport := disabled.HTTPClient().Transport.(*http.Transport)
	if disabledTransport.Proxy != nil {
		t.Fatal("WithoutProxy after WithProxyFromEnvironment did not disable proxy")
	}
}

func TestWithProxyFunc(t *testing.T) {
	proxiedTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "proxied")
	}))
	defer proxiedTarget.Close()

	directTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "direct")
	}))
	defer directTarget.Close()

	proxy := newTestProxy(t, "", "")
	proxyURL, err := url.Parse(proxy.server.URL)
	if err != nil {
		t.Fatalf("parse proxy URL: %v", err)
	}
	proxiedHost, err := url.Parse(proxiedTarget.URL)
	if err != nil {
		t.Fatalf("parse target URL: %v", err)
	}

	var calls atomic.Int32
	client, err := req.NewWithOptions(req.WithProxyFunc(
		func(r *http.Request) (*url.URL, error) {
			calls.Add(1)
			if r.URL.Host == proxiedHost.Host {
				return proxyURL, nil
			}
			return nil, nil
		},
	))
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}
	t.Cleanup(client.CloseIdleConnections)

	body, err := client.Get(context.Background(), proxiedTarget.URL).ToString()
	if err != nil {
		t.Fatalf("ToString proxied: %v", err)
	}
	if body != "proxied" {
		t.Fatalf("proxied body = %q", body)
	}
	if proxy.httpRequestCount() != 1 {
		t.Fatalf("proxy saw %d HTTP requests, want 1", proxy.httpRequestCount())
	}

	body, err = client.Get(context.Background(), directTarget.URL).ToString()
	if err != nil {
		t.Fatalf("ToString direct: %v", err)
	}
	if body != "direct" {
		t.Fatalf("direct body = %q", body)
	}
	if proxy.httpRequestCount() != 1 {
		t.Fatalf("direct request went through proxy: %d requests", proxy.httpRequestCount())
	}
	if calls.Load() < 2 {
		t.Fatalf("proxy function calls = %d, want at least 2", calls.Load())
	}
}

func TestProxyConfigurationConflicts(t *testing.T) {
	customTransport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("not called")
	})
	proxy := "http://127.0.0.1:8080"
	httpClient := &http.Client{}

	tests := []struct {
		name string
		opts []req.Option
	}{
		{name: "http client and proxy", opts: []req.Option{req.WithHTTPClient(httpClient), req.WithProxy(proxy)}},
		{name: "proxy and http client", opts: []req.Option{req.WithProxy(proxy), req.WithHTTPClient(httpClient)}},
		{name: "http client and without proxy", opts: []req.Option{req.WithHTTPClient(httpClient), req.WithoutProxy()}},
		{name: "http client and proxy from environment", opts: []req.Option{req.WithHTTPClient(httpClient), req.WithProxyFromEnvironment()}},
		{name: "http client and proxy func", opts: []req.Option{req.WithHTTPClient(httpClient), req.WithProxyFunc(func(*http.Request) (*url.URL, error) { return nil, nil })}},
		{name: "custom transport and proxy", opts: []req.Option{req.WithTransport(customTransport), req.WithProxy(proxy)}},
		{name: "proxy and custom transport", opts: []req.Option{req.WithProxy(proxy), req.WithTransport(customTransport)}},
		{name: "custom transport and without proxy", opts: []req.Option{req.WithTransport(customTransport), req.WithoutProxy()}},
		{name: "custom transport and proxy from environment", opts: []req.Option{req.WithTransport(customTransport), req.WithProxyFromEnvironment()}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, err := req.NewWithOptions(tc.opts...)
			if err == nil || client != nil {
				t.Fatalf("NewWithOptions = (%v, %v), want (nil, error)", client, err)
			}
			if !errors.Is(err, req.ErrConfiguration) {
				t.Fatalf("err = %v, want ErrConfiguration", err)
			}

			fallback := req.New(tc.opts...)
			if !errors.Is(fallback.Err(), req.ErrConfiguration) {
				t.Fatalf("New Err = %v, want ErrConfiguration", fallback.Err())
			}
		})
	}
}

func TestProxyOptionOrderDoesNotChangeSemantics(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ordered")
	}))
	defer target.Close()

	proxy := newTestProxy(t, "", "")
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil

	tests := []struct {
		name string
		opts []req.Option
	}{
		{
			name: "proxy before transport",
			opts: []req.Option{
				req.WithProxy(proxy.server.URL),
				req.WithTransport(transport),
				req.WithTimeout(3 * time.Second),
			},
		},
		{
			name: "transport before proxy",
			opts: []req.Option{
				req.WithTransport(transport),
				req.WithTimeout(3 * time.Second),
				req.WithProxy(proxy.server.URL),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, err := req.NewWithOptions(tc.opts...)
			if err != nil {
				t.Fatalf("NewWithOptions: %v", err)
			}
			t.Cleanup(client.CloseIdleConnections)

			built, ok := client.HTTPClient().Transport.(*http.Transport)
			if !ok {
				t.Fatalf("transport = %T", client.HTTPClient().Transport)
			}
			if built == transport {
				t.Fatal("supplied transport was not cloned")
			}
			if transport.Proxy != nil {
				t.Fatal("supplied transport Proxy was modified")
			}
			if built.Proxy == nil {
				t.Fatal("cloned transport has no Proxy")
			}

			body, err := client.Get(context.Background(), target.URL).ToString()
			if err != nil {
				t.Fatalf("ToString: %v", err)
			}
			if body != "ordered" {
				t.Fatalf("body = %q", body)
			}
		})
	}

	if proxy.httpRequestCount() != 2 {
		t.Fatalf("proxy saw %d HTTP requests, want 2", proxy.httpRequestCount())
	}
}

func TestWithProxyAndDialContext(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "dialed-proxy")
	}))
	defer target.Close()

	proxy := newTestProxy(t, "", "")
	var dials atomic.Int32
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		dials.Add(1)
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}

	tests := []struct {
		name string
		opts []req.Option
	}{
		{
			name: "proxy before dial",
			opts: []req.Option{
				req.WithProxy(proxy.server.URL),
				req.WithDialContext(dial),
			},
		},
		{
			name: "dial before proxy",
			opts: []req.Option{
				req.WithDialContext(dial),
				req.WithProxy(proxy.server.URL),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, err := req.NewWithOptions(tc.opts...)
			if err != nil {
				t.Fatalf("NewWithOptions: %v", err)
			}
			t.Cleanup(client.CloseIdleConnections)

			body, err := client.Get(context.Background(), target.URL).ToString()
			if err != nil {
				t.Fatalf("ToString: %v", err)
			}
			if body != "dialed-proxy" {
				t.Fatalf("body = %q", body)
			}
		})
	}
	if dials.Load() == 0 {
		t.Fatal("custom DialContext was not used for proxy connections")
	}
}

func TestInvalidProxyOptions(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "empty", raw: ""},
		{name: "whitespace", raw: "   "},
		{name: "relative", raw: "proxy.example.com:8080"},
		{name: "unsupported scheme", raw: "ftp://proxy.example.com:21"},
		{name: "missing host", raw: "http://"},
		{name: "missing hostname", raw: "http://:8080"},
		{name: "invalid port text", raw: "http://proxy.example.com:notaport"},
		{name: "invalid port with credentials", raw: "http://user:secret@proxy.example.com:notaport"},
		{name: "port zero", raw: "http://proxy.example.com:0"},
		{name: "port too large", raw: "http://proxy.example.com:65536"},
		{name: "empty port", raw: "http://proxy.example.com:"},
		{name: "path", raw: "http://proxy.example.com:8080/not-allowed"},
		{name: "query", raw: "http://proxy.example.com:8080?x=1"},
		{name: "fragment", raw: "http://proxy.example.com:8080#frag"},
		{name: "socks5 without port", raw: "socks5://proxy.example.com"},
		{name: "socks5h without port", raw: "socks5h://proxy.example.com"},
		{name: "credentials missing host", raw: "http://user:secret@:8080"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, err := req.NewWithOptions(req.WithProxy(tc.raw))
			if err == nil || client != nil {
				t.Fatalf("NewWithOptions = (%v, %v), want (nil, error)", client, err)
			}
			if !errors.Is(err, req.ErrConfiguration) {
				t.Fatalf("err = %v, want ErrConfiguration", err)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatalf("configuration error leaked proxy password: %v", err)
			}
		})
	}

	if _, err := req.NewWithOptions(req.WithProxyFunc(nil)); err == nil ||
		!errors.Is(err, req.ErrConfiguration) {
		t.Fatalf("WithProxyFunc(nil) err = %v, want ErrConfiguration", err)
	}
}

func TestWithProxyAcceptedSchemes(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "http with port", raw: "http://proxy.example.com:8080", want: "http://proxy.example.com:8080"},
		{name: "http default port", raw: "http://proxy.example.com", want: "http://proxy.example.com"},
		{name: "https", raw: "https://proxy.example.com:8443", want: "https://proxy.example.com:8443"},
		{name: "trailing slash", raw: "http://proxy.example.com:8080/", want: "http://proxy.example.com:8080/"},
		{name: "socks5", raw: "socks5://user:pass@proxy.example.com:1080", want: "socks5://user:pass@proxy.example.com:1080"},
		{name: "socks5h", raw: "socks5h://proxy.example.com:1080", want: "socks5h://proxy.example.com:1080"},
		{name: "ipv6", raw: "http://[::1]:8080", want: "http://[::1]:8080"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, err := req.NewWithOptions(req.WithProxy(tc.raw))
			if err != nil {
				t.Fatalf("NewWithOptions: %v", err)
			}
			transport, ok := client.HTTPClient().Transport.(*http.Transport)
			if !ok {
				t.Fatalf("transport = %T", client.HTTPClient().Transport)
			}
			if transport.Proxy == nil {
				t.Fatal("Proxy is nil")
			}
			reqURL, _ := http.NewRequest(http.MethodGet, "http://example.com", nil)
			u, err := transport.Proxy(reqURL)
			if err != nil {
				t.Fatalf("Proxy: %v", err)
			}
			if u == nil || u.String() != tc.want {
				t.Fatalf("proxy URL = %v, want %q", u, tc.want)
			}
		})
	}
}

func TestProxyCredentialsNotLogged(t *testing.T) {
	const (
		username = "debug-proxy-user"
		password = "debug-proxy-password"
	)

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer target.Close()

	proxy := newTestProxy(t, username, password)
	var output bytes.Buffer
	client, err := req.NewWithOptions(
		req.WithProxy(proxyURLWithCredentials(t, proxy.server.URL, username, password)),
		req.WithDebug(true),
		req.WithDebugWriter(&output),
		req.WithDebugBody(false),
	)
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}
	t.Cleanup(client.CloseIdleConnections)

	if _, err := client.Get(context.Background(), target.URL).ToString(); err != nil {
		t.Fatalf("ToString: %v", err)
	}

	logged := output.String()
	if logged == "" {
		t.Fatal("debug output is empty")
	}
	for _, secret := range []string{username, password, "Proxy-Authorization", "Basic "} {
		if strings.Contains(logged, secret) {
			t.Fatalf("debug output leaked %q:\n%s", secret, logged)
		}
	}
}

func TestDefaultProxyBehaviorIsEnvironment(t *testing.T) {
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t.Skip("http.DefaultTransport is not *http.Transport")
	}
	defaultProxy := transport.Proxy

	client, err := req.NewWithOptions()
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}
	clientTransport := client.HTTPClient().Transport.(*http.Transport)
	if clientTransport == transport {
		t.Fatal("client shares http.DefaultTransport")
	}
	if clientTransport.Proxy == nil {
		t.Fatal("default client has no Proxy")
	}
	if reflect.ValueOf(clientTransport.Proxy).Pointer() != reflect.ValueOf(http.ProxyFromEnvironment).Pointer() {
		t.Fatal("default client does not use http.ProxyFromEnvironment")
	}
	if reflect.ValueOf(transport.Proxy).Pointer() != reflect.ValueOf(defaultProxy).Pointer() {
		t.Fatal("http.DefaultTransport.Proxy was modified")
	}
}
