package req_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/Xwudao/go-req"
)

func TestQuerySemantics(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(r.URL.Query())
	}))
	defer srv.Close()
	client := req.New(req.WithBaseURL(srv.URL))
	ctx := context.Background()

	t.Run("set append and URL query", func(t *testing.T) {
		var got url.Values
		err := client.Get(ctx, "/users?page=0&keep=x&remove=1").
			Query("page", "1").
			AddQuery("tag", "go").
			AddQuery("tag", "http").
			QueryValues(url.Values{"limit": {"20"}, "remove": nil}).
			ToJSON(&got)
		if err != nil {
			t.Fatalf("ToJSON: %v", err)
		}
		if got.Get("page") != "1" {
			t.Errorf("page = %q", got.Get("page"))
		}
		if got.Get("keep") != "x" {
			t.Errorf("keep = %q", got.Get("keep"))
		}
		if tags := got["tag"]; len(tags) != 2 || tags[0] != "go" || tags[1] != "http" {
			t.Errorf("tag = %#v", tags)
		}
		if got.Get("limit") != "20" {
			t.Errorf("limit = %q", got.Get("limit"))
		}
		if _, exists := got["remove"]; exists {
			t.Errorf("remove was not deleted: %#v", got)
		}
	})

	t.Run("call order", func(t *testing.T) {
		var got url.Values
		err := client.Get(ctx, "/order").
			AddQuery("x", "2").
			Query("x", "1").
			ToJSON(&got)
		if err != nil {
			t.Fatalf("ToJSON: %v", err)
		}
		if values := got["x"]; len(values) != 1 || values[0] != "1" {
			t.Errorf("x = %#v", values)
		}

		err = client.Get(ctx, "/order").
			Query("x", "1").
			AddQuery("x", "2").
			ToJSON(&got)
		if err != nil {
			t.Fatalf("ToJSON: %v", err)
		}
		if values := got["x"]; len(values) != 2 || values[0] != "1" || values[1] != "2" {
			t.Errorf("x = %#v", values)
		}
	})

	t.Run("encoding", func(t *testing.T) {
		var got url.Values
		err := client.Get(ctx, "/encode").
			Query("q", "a b&c=d").
			Query("unicode", "你好").
			ToJSON(&got)
		if err != nil {
			t.Fatalf("ToJSON: %v", err)
		}
		if got.Get("q") != "a b&c=d" {
			t.Errorf("q = %q", got.Get("q"))
		}
		if got.Get("unicode") != "你好" {
			t.Errorf("unicode = %q", got.Get("unicode"))
		}
	})
}

func TestAbsoluteURLQueryMerging(t *testing.T) {
	client := req.New(req.WithBaseURL("https://api.example.com/v1"))
	built, err := client.Get(context.Background(), "https://other.example.com/users?a=1").
		Query("a", "2").
		AddQuery("b", "3").
		Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got, want := built.URL.String(), "https://other.example.com/users?a=2&b=3"; got != want {
		t.Fatalf("URL = %q, want %q", got, want)
	}
}

func TestInvalidQuery(t *testing.T) {
	client := req.New()
	_, err := client.Get(context.Background(), "https://example.com/?a=1;b=2").
		Query("x", "1").
		Build()
	if err == nil {
		t.Fatal("expected invalid query error")
	}
}

func TestJSONRequestCanBeExecutedTwice(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(body))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	client := req.New(req.WithBaseURL(srv.URL))
	builder := client.Post(context.Background(), "/users").JSON(map[string]string{"name": "Alice"})
	for i := 0; i < 2; i++ {
		if _, err := builder.ToBytes(); err != nil {
			t.Fatalf("execution %d: %v", i, err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 || bodies[0] != `{"name":"Alice"}` || bodies[1] != bodies[0] {
		t.Fatalf("bodies = %#v", bodies)
	}
}

func TestBuildIsRepeatableForJSONAndForm(t *testing.T) {
	client := req.New()
	ctx := context.Background()

	tests := []struct {
		name string
		req  *req.Request
		want string
	}{
		{
			name: "json",
			req:  client.Post(ctx, "https://example.com").JSON(map[string]string{"name": "Alice"}),
			want: `{"name":"Alice"}`,
		},
		{
			name: "form",
			req:  client.Post(ctx, "https://example.com").Form(url.Values{"name": {"Alice"}}),
			want: "name=Alice",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for i := 0; i < 2; i++ {
				built, err := tc.req.Build()
				if err != nil {
					t.Fatalf("Build %d: %v", i, err)
				}
				body, err := io.ReadAll(built.Body)
				if err != nil {
					t.Fatalf("read body: %v", err)
				}
				_ = built.Body.Close()
				if string(body) != tc.want {
					t.Fatalf("build %d body = %q, want %q", i, body, tc.want)
				}
			}
		})
	}
}

func TestBuildHeaderAndQueryAreIndependent(t *testing.T) {
	client := req.New(req.WithHeader("X-Default", "client"))
	builder := client.Get(context.Background(), "https://example.com/users").
		Header("X-Test", "one").
		Query("page", "1")

	first, err := builder.Build()
	if err != nil {
		t.Fatalf("first Build: %v", err)
	}
	first.Header.Set("X-Test", "changed")
	first.Header.Set("X-Default", "changed")
	first.URL.RawQuery = "page=99"
	first.URL.Path = "/changed"

	second, err := builder.Build()
	if err != nil {
		t.Fatalf("second Build: %v", err)
	}
	if got := second.Header.Get("X-Test"); got != "one" {
		t.Fatalf("X-Test = %q", got)
	}
	if got := second.Header.Get("X-Default"); got != "client" {
		t.Fatalf("X-Default = %q", got)
	}
	if got := second.URL.String(); got != "https://example.com/users?page=1" {
		t.Fatalf("URL = %q", got)
	}
}

func TestBodyReaderSingleConsumption(t *testing.T) {
	var mu sync.Mutex
	var received []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		received = append(received, string(body))
		mu.Unlock()
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	client := req.New(req.WithBaseURL(srv.URL))
	builder := client.Post(context.Background(), "/echo").Body(strings.NewReader("hello"))
	first, err := builder.ToString()
	if err != nil {
		t.Fatalf("first ToString: %v", err)
	}
	if first != "hello" {
		t.Fatalf("first body = %q", first)
	}
	second, err := builder.ToString()
	if err != nil {
		t.Fatalf("second ToString: %v", err)
	}
	if second != "" {
		t.Fatalf("second body = %q, want empty", second)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 2 || received[0] != "hello" || received[1] != "" {
		t.Fatalf("received = %#v", received)
	}
}

func TestBodyBuildSharesReader(t *testing.T) {
	client := req.New()
	builder := client.Post(context.Background(), "https://example.com").Body(strings.NewReader("abc"))

	first, err := builder.Build()
	if err != nil {
		t.Fatalf("first Build: %v", err)
	}
	firstBody, _ := io.ReadAll(first.Body)
	_ = first.Body.Close()
	if string(firstBody) != "abc" {
		t.Fatalf("first body = %q", firstBody)
	}

	second, err := builder.Build()
	if err != nil {
		t.Fatalf("second Build: %v", err)
	}
	secondBody, _ := io.ReadAll(second.Body)
	_ = second.Body.Close()
	if len(secondBody) != 0 {
		t.Fatalf("second body = %q, want empty", secondBody)
	}
}

type closeTrackingReader struct {
	reader io.Reader
	closed bool
}

func (r *closeTrackingReader) Read(p []byte) (int, error) {
	return r.reader.Read(p)
}

func (r *closeTrackingReader) Close() error {
	r.closed = true
	return nil
}

func TestBodyReaderIsNotClosedByReq(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	reader := &closeTrackingReader{reader: strings.NewReader("payload")}
	client := req.New(req.WithBaseURL(srv.URL))
	if _, err := client.Post(context.Background(), "/upload").Body(reader).ToBytes(); err != nil {
		t.Fatalf("ToBytes: %v", err)
	}
	if reader.closed {
		t.Fatal("req closed the caller's reader")
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestJSONEncodeErrorIsDelayed(t *testing.T) {
	client := req.New()
	builder := client.Post(context.Background(), "https://example.com").
		JSON(make(chan int)).
		Header("X-After-Error", "ignored")
	if got := builder; got == nil {
		t.Fatal("builder is nil")
	}

	_, err := builder.Build()
	if err == nil {
		t.Fatal("expected encoding error")
	}
	var unsupported *json.UnsupportedTypeError
	if !errors.As(err, &unsupported) {
		t.Fatalf("err = %T, want *json.UnsupportedTypeError", err)
	}

	// The first error is retained.
	_, err = client.Get(context.Background(), "://bad").JSON(make(chan int)).Build()
	if !errors.As(err, &unsupported) {
		t.Fatalf("first error was not retained: %v", err)
	}
}

func TestNilCookieError(t *testing.T) {
	_, err := req.New().Get(context.Background(), "https://example.com").Cookie(nil).Build()
	if err == nil || !strings.Contains(err.Error(), "nil cookie") {
		t.Fatalf("err = %v", err)
	}
}

func TestInvalidMethod(t *testing.T) {
	_, err := req.New().NewRequest(context.Background(), "BAD METHOD", "https://example.com").Build()
	if err == nil {
		t.Fatal("expected invalid method error")
	}
}

func TestClientDoNativeRequest(t *testing.T) {
	var gotPath, gotDefaultHeader, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotDefaultHeader = r.Header.Get("X-Default")
		gotMethod = r.Method
		_, _ = io.WriteString(w, "raw")
	}))
	defer srv.Close()

	client := req.New(
		req.WithBaseURL(srv.URL+"/api"),
		req.WithHeader("X-Default", "should-not-apply"),
	)
	raw, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/raw", bytes.NewBufferString("body"))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := client.Do(raw)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "raw" {
		t.Fatalf("body = %q", body)
	}
	if gotPath != "/raw" {
		t.Fatalf("path = %q, BaseURL polluted native Do", gotPath)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method = %q", gotMethod)
	}
	if gotDefaultHeader != "" {
		t.Fatalf("default header polluted native Do: %q", gotDefaultHeader)
	}
}

func TestClientDoHTTPErrorIsNotTransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := req.New()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do returned transport error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestClientDoNilRequest(t *testing.T) {
	if _, err := req.New().Do(nil); err == nil {
		t.Fatal("expected error for nil request")
	}
}
