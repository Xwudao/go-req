package req_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Xwudao/go-req"
)

func TestGetToJSON(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":1,"name":"Alice"}`)
	}))
	defer srv.Close()

	client := req.New(req.WithBaseURL(srv.URL), req.WithHeader("User-Agent", "go-req"))

	var user struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	if err := client.Get(context.Background(), "/users/1").ToJSON(&user); err != nil {
		t.Fatalf("ToJSON: %v", err)
	}
	if gotPath != "/users/1" {
		t.Fatalf("path = %q, want /users/1", gotPath)
	}
	if user.ID != 1 || user.Name != "Alice" {
		t.Fatalf("user = %+v", user)
	}
}

func TestHTTPMethods(t *testing.T) {
	var mu sync.Mutex
	got := make(map[string]string)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		got[r.Method] = string(body)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	client := req.New(req.WithBaseURL(srv.URL))
	ctx := context.Background()

	for _, method := range []string{
		http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete,
	} {
		method := method
		t.Run(method, func(t *testing.T) {
			body := ""
			if method != http.MethodGet {
				body = method
			}
			result, err := client.NewRequest(ctx, method, "/resource").Body(strings.NewReader(body)).ToString()
			if err != nil {
				t.Fatalf("%s: %v", method, err)
			}
			if result != "" {
				t.Fatalf("%s result = %q", method, result)
			}
			mu.Lock()
			defer mu.Unlock()
			if got[method] != body {
				t.Fatalf("%s body = %q, want %q", method, got[method], body)
			}
		})
	}

	resp, err := client.Head(ctx, "/resource").Do()
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD status = %d", resp.StatusCode)
	}
}

func TestPostJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q", ct)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if payload["name"] != "Alice" {
			t.Errorf("payload = %#v", payload)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":7}`)
	}))
	defer srv.Close()

	client := req.New(req.WithBaseURL(srv.URL))
	var result struct {
		ID int `json:"id"`
	}
	err := client.Post(context.Background(), "/users").
		JSON(map[string]any{"name": "Alice", "age": 25}).
		ToJSON(&result)
	if err != nil {
		t.Fatalf("ToJSON: %v", err)
	}
	if result.ID != 7 {
		t.Fatalf("result = %+v", result)
	}
}

func TestContentTypeOverride(t *testing.T) {
	client := req.New()
	tests := []struct {
		name string
		req  *req.Request
		want string
	}{
		{
			name: "json then header",
			req: client.Post(context.Background(), "https://example.com").
				JSON(map[string]string{"a": "b"}).
				Header("Content-Type", "application/vnd.api+json"),
			want: "application/vnd.api+json",
		},
		{
			name: "header then json",
			req: client.Post(context.Background(), "https://example.com").
				Header("Content-Type", "application/vnd.api+json").
				JSON(map[string]string{"a": "b"}),
			want: "application/vnd.api+json",
		},
		{
			name: "form default",
			req:  client.Post(context.Background(), "https://example.com").Form(url.Values{"a": {"b"}}),
			want: "application/x-www-form-urlencoded",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			built, err := tc.req.Build()
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if got := built.Header.Get("Content-Type"); got != tc.want {
				t.Fatalf("Content-Type = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHeadersAndDefaults(t *testing.T) {
	var mu sync.Mutex
	var headers []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		headers = append(headers, r.Header.Clone())
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	client := req.New(
		req.WithBaseURL(srv.URL),
		req.WithHeader("X-Default", "client"),
		req.WithHeader("User-Agent", "go-req"),
	)
	if _, err := client.Get(context.Background(), "/one").
		Header("X-Default", "request").
		Headers(http.Header{"X-Multi": {"a", "b"}}).
		ToBytes(); err != nil {
		t.Fatalf("first request: %v", err)
	}
	if _, err := client.Get(context.Background(), "/two").ToBytes(); err != nil {
		t.Fatalf("second request: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(headers) != 2 {
		t.Fatalf("requests = %d", len(headers))
	}
	if got := headers[0].Get("X-Default"); got != "request" {
		t.Errorf("first X-Default = %q", got)
	}
	if got := headers[1].Get("X-Default"); got != "client" {
		t.Errorf("second X-Default = %q", got)
	}
	if got := headers[0].Values("X-Multi"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("X-Multi = %#v", got)
	}
	if got := headers[1].Values("X-Multi"); len(got) != 0 {
		t.Errorf("X-Multi leaked to second request: %#v", got)
	}
	if got := headers[1].Get("User-Agent"); got != "go-req" {
		t.Errorf("User-Agent = %q", got)
	}
}

func TestCookiesAndAuthorization(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/basic":
			user, pass, ok := r.BasicAuth()
			if !ok || user != "tim" || pass != "s3cret" {
				t.Errorf("BasicAuth = %q, %q, %v", user, pass, ok)
			}
		case "/bearer":
			if got := r.Header.Get("Authorization"); got != "Bearer token-123" {
				t.Errorf("Authorization = %q", got)
			}
		case "/cookies":
			cookies := r.Cookies()
			if len(cookies) != 2 || cookies[0].Name != "a" || cookies[0].Value != "1" ||
				cookies[1].Name != "b" || cookies[1].Value != "2" {
				t.Errorf("cookies = %#v", cookies)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	client := req.New(req.WithBaseURL(srv.URL))
	ctx := context.Background()

	if _, err := client.Get(ctx, "/basic").BasicAuth("tim", "s3cret").ToBytes(); err != nil {
		t.Fatalf("basic: %v", err)
	}
	if _, err := client.Get(ctx, "/bearer").BearerToken("token-123").ToBytes(); err != nil {
		t.Fatalf("bearer: %v", err)
	}
	if _, err := client.Get(ctx, "/cookies").
		Cookie(&http.Cookie{Name: "a", Value: "1"}).
		Cookie(&http.Cookie{Name: "b", Value: "2"}).
		ToBytes(); err != nil {
		t.Fatalf("cookies: %v", err)
	}
}

func TestForm(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		if r.PostForm.Get("name") != "Alice" || len(r.PostForm["tag"]) != 2 {
			t.Errorf("form = %#v", r.PostForm)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	client := req.New(req.WithBaseURL(srv.URL))
	if _, err := client.Post(context.Background(), "/form").
		Form(url.Values{"name": {"Alice"}, "tag": {"go", "http"}}).
		ToBytes(); err != nil {
		t.Fatalf("ToBytes: %v", err)
	}
}

func TestBaseURLResolution(t *testing.T) {
	tests := []struct {
		name     string
		base     string
		raw      string
		want     string
		wantPath string
	}{
		{name: "relative", base: "https://api.example.com/v1", raw: "users", want: "https://api.example.com/v1/users"},
		{name: "leading slash", base: "https://api.example.com/v1", raw: "/users", want: "https://api.example.com/v1/users"},
		{name: "empty path", base: "https://api.example.com/v1", raw: "", want: "https://api.example.com/v1"},
		{name: "root path", base: "https://api.example.com/v1", raw: "/", want: "https://api.example.com/v1/"},
		{name: "trailing slash", base: "https://api.example.com/v1", raw: "users/", want: "https://api.example.com/v1/users/"},
		{name: "dot segments", base: "https://api.example.com/v1", raw: "../users", want: "https://api.example.com/users"},
		{name: "base with trailing slash", base: "https://api.example.com/v1/", raw: "users", want: "https://api.example.com/v1/users"},
		{name: "base without path", base: "https://api.example.com", raw: "users", want: "https://api.example.com/users"},
		{name: "absolute ignores base", base: "https://api.example.com/v1", raw: "https://other.example.com/users?x=1#frag", want: "https://other.example.com/users?x=1#frag"},
		{name: "scheme relative", base: "https://api.example.com/v1", raw: "//other.example.com/users", want: "https://other.example.com/users"},
		{name: "fragment", base: "https://api.example.com/v1", raw: "/users#section", want: "https://api.example.com/v1/users#section"},
		{
			name:     "escaped slash",
			base:     "https://api.example.com/v1",
			raw:      "/users/a%2Fb",
			want:     "https://api.example.com/v1/users/a%2Fb",
			wantPath: "/v1/users/a/b",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := req.New(req.WithBaseURL(tc.base))
			built, err := client.Get(context.Background(), tc.raw).Build()
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if got := built.URL.String(); got != tc.want {
				t.Fatalf("URL = %q, want %q", got, tc.want)
			}
			if tc.wantPath != "" && built.URL.Path != tc.wantPath {
				t.Fatalf("Path = %q, want %q", built.URL.Path, tc.wantPath)
			}
		})
	}
}

func TestRelativeURLWithoutBase(t *testing.T) {
	_, err := req.New().Get(context.Background(), "/users").Build()
	if !errors.Is(err, req.ErrNoBaseURL) {
		t.Fatalf("err = %v, want ErrNoBaseURL", err)
	}
	_, err = req.New().Get(context.Background(), "").Build()
	if !errors.Is(err, req.ErrNoBaseURL) {
		t.Fatalf("empty URL err = %v, want ErrNoBaseURL", err)
	}
}

func TestInvalidURL(t *testing.T) {
	client := req.New(req.WithBaseURL("https://api.example.com"))
	_, err := client.Get(context.Background(), "://bad").Build()
	if err == nil {
		t.Fatal("expected error")
	}
	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		t.Fatalf("err = %T, want *url.Error", err)
	}
}

func TestNewWithOptionsValid(t *testing.T) {
	hc := &http.Client{Timeout: 3 * time.Second}
	client, err := req.NewWithOptions(
		req.WithHTTPClient(hc),
		req.WithBaseURL("https://api.example.com"),
		req.WithHeader("X-Test", "1"),
	)
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}
	if client.HTTPClient() != hc {
		t.Fatal("HTTPClient is not the supplied client")
	}
	if client.Err() != nil {
		t.Fatalf("Err = %v", client.Err())
	}
	built, err := client.Get(context.Background(), "users").Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if built.URL.String() != "https://api.example.com/users" {
		t.Fatalf("URL = %s", built.URL)
	}
	if got := built.Header.Get("X-Test"); got != "1" {
		t.Fatalf("X-Test = %q", got)
	}
}

func TestConfigurationConflicts(t *testing.T) {
	hc := &http.Client{}
	rt := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("not called")
	})
	dial := func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("not called")
	}
	tests := []struct {
		name string
		opts []req.Option
	}{
		{name: "http client and timeout", opts: []req.Option{req.WithHTTPClient(hc), req.WithTimeout(time.Second)}},
		{name: "timeout and http client", opts: []req.Option{req.WithTimeout(time.Second), req.WithHTTPClient(hc)}},
		{name: "http client and transport", opts: []req.Option{req.WithHTTPClient(hc), req.WithTransport(rt)}},
		{name: "http client and dial", opts: []req.Option{req.WithHTTPClient(hc), req.WithDialContext(dial)}},
		{name: "transport and dial", opts: []req.Option{req.WithTransport(rt), req.WithDialContext(dial)}},
		{name: "dial and transport", opts: []req.Option{req.WithDialContext(dial), req.WithTransport(rt)}},
		{name: "nil option", opts: []req.Option{nil}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, err := req.NewWithOptions(tc.opts...)
			if err == nil {
				t.Fatal("NewWithOptions succeeded, want error")
			}
			if client != nil {
				t.Fatalf("client = %v, want nil", client)
			}
			if !errors.Is(err, req.ErrConfiguration) {
				t.Fatalf("err = %v, want ErrConfiguration", err)
			}

			// New must retain the error and surface it, not panic or ignore it.
			fallback := req.New(tc.opts...)
			if !errors.Is(fallback.Err(), req.ErrConfiguration) {
				t.Fatalf("New Err = %v", fallback.Err())
			}
			_, buildErr := fallback.Get(context.Background(), "https://example.com").Build()
			if !errors.Is(buildErr, req.ErrConfiguration) {
				t.Fatalf("Build err = %v, want ErrConfiguration", buildErr)
			}
			_, doErr := fallback.Do(&http.Request{})
			if !errors.Is(doErr, req.ErrConfiguration) {
				t.Fatalf("Do err = %v, want ErrConfiguration", doErr)
			}
		})
	}
}

func TestInvalidOptions(t *testing.T) {
	tests := []struct {
		name string
		opt  req.Option
	}{
		{name: "relative base", opt: req.WithBaseURL("api.example.com")},
		{name: "base without host", opt: req.WithBaseURL("https:///v1")},
		{name: "base with query", opt: req.WithBaseURL("https://api.example.com/v1?x=1")},
		{name: "base with fragment", opt: req.WithBaseURL("https://api.example.com/v1#frag")},
		{name: "invalid base", opt: req.WithBaseURL("://bad")},
		{name: "negative timeout", opt: req.WithTimeout(-time.Second)},
		{name: "nil http client", opt: req.WithHTTPClient(nil)},
		{name: "nil transport", opt: req.WithTransport(nil)},
		{name: "nil dial", opt: req.WithDialContext(nil)},
		{name: "zero max response", opt: req.WithMaxResponseBytes(0)},
		{name: "negative max response", opt: req.WithMaxResponseBytes(-1)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, err := req.NewWithOptions(tc.opt)
			if err == nil || client != nil {
				t.Fatalf("NewWithOptions = (%v, %v), want (nil, error)", client, err)
			}
			if !errors.Is(err, req.ErrConfiguration) {
				t.Fatalf("err = %v, want ErrConfiguration", err)
			}
		})
	}
}

func TestOptionsLastOneWins(t *testing.T) {
	client := req.New(
		req.WithBaseURL("https://first.example.com"),
		req.WithBaseURL("https://second.example.com"),
		req.WithHeader("X-Test", "one"),
		req.WithHeader("X-Test", "two"),
	)
	built, err := client.Get(context.Background(), "users").Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got, want := built.URL.String(), "https://second.example.com/users"; got != want {
		t.Fatalf("URL = %q, want %q", got, want)
	}
	if got := built.Header.Get("X-Test"); got != "two" {
		t.Fatalf("X-Test = %q", got)
	}
}

func TestDefaultTransportIsIndependent(t *testing.T) {
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t.Skip("http.DefaultTransport is not *http.Transport")
	}
	client := req.New()
	if client.HTTPClient() == http.DefaultClient {
		t.Fatal("client shares http.DefaultClient")
	}
	transport, ok := client.HTTPClient().Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T", client.HTTPClient().Transport)
	}
	if transport == defaultTransport {
		t.Fatal("client shares http.DefaultTransport")
	}
}

func TestTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		_, _ = io.WriteString(w, "late")
	}))
	defer srv.Close()

	client := req.New(req.WithBaseURL(srv.URL), req.WithTimeout(20*time.Millisecond))
	if got := client.HTTPClient().Timeout; got != 20*time.Millisecond {
		t.Fatalf("Timeout = %v", got)
	}
	_, err := client.Get(context.Background(), "/slow").ToBytes()
	if err == nil {
		t.Fatal("expected timeout")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
}

func TestContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := req.New(req.WithBaseURL("https://example.com"))
	_, err := client.Get(ctx, "/").ToBytes()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestContextDeadlineExceeded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = io.WriteString(w, "late")
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	client := req.New(req.WithBaseURL(srv.URL))
	_, err := client.Get(ctx, "/slow").ToBytes()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
}

func TestNilContext(t *testing.T) {
	client := req.New()
	_, err := client.Get(context.Background(), "https://example.com").Build()
	if err != nil {
		t.Fatalf("sanity check: %v", err)
	}
	var nilCtx context.Context
	_, err = client.Get(nilCtx, "https://example.com").Build()
	if err == nil || !strings.Contains(err.Error(), "nil context") {
		t.Fatalf("err = %v", err)
	}
}

func TestConnectionFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	addr := srv.URL
	srv.Close()

	client := req.New()
	_, err := client.Get(context.Background(), addr).ToBytes()
	if err == nil {
		t.Fatal("expected connection error")
	}
	var statusErr *req.StatusError
	if errors.As(err, &statusErr) {
		t.Fatalf("connection error reported as StatusError: %v", err)
	}
	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		t.Fatalf("err = %T, want *url.Error", err)
	}
}

func TestSharedClientConcurrent(t *testing.T) {
	const requests = 40
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if got := r.Header.Get("X-ID"); got != id {
			t.Errorf("X-ID = %q, query id = %q", got, id)
		}
		fmt.Fprintf(w, `{"id":%q}`, id)
	}))
	defer srv.Close()

	client := req.New(
		req.WithBaseURL(srv.URL),
		req.WithHeader("X-Default", "shared"),
	)
	var wg sync.WaitGroup
	errs := make(chan error, requests)
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("%d", i)
			var response struct {
				ID string `json:"id"`
			}
			err := client.Get(context.Background(), "/work").
				Query("id", id).
				Header("X-ID", id).
				ToJSON(&response)
			if err != nil {
				errs <- err
				return
			}
			if response.ID != id {
				errs <- fmt.Errorf("id = %q, want %q", response.ID, id)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
