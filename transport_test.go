package req_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Xwudao/go-req"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

type recordingTransport struct {
	calls    int
	last     *http.Request
	response string
	status   int
}

func (t *recordingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.calls++
	t.last = r
	status := t.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(t.response)),
		Request:    r,
	}, nil
}

func TestWithTransport(t *testing.T) {
	transport := &recordingTransport{response: `{"source":"custom"}`}
	client := req.New(
		req.WithTransport(transport),
		req.WithBaseURL("https://api.example.com/v1"),
		req.WithHeader("X-Default", "yes"),
		req.WithTimeout(2*time.Second),
	)
	if client.HTTPClient().Transport != transport {
		t.Fatal("custom transport was replaced")
	}
	if client.HTTPClient().Timeout != 2*time.Second {
		t.Fatalf("Timeout = %v", client.HTTPClient().Timeout)
	}

	out, err := client.Get(context.Background(), "/custom").ToJSON[struct {
		Source string `json:"source"`
	}]()
	if err != nil {
		t.Fatalf("ToJSON: %v", err)
	}
	if out.Source != "custom" {
		t.Fatalf("out = %+v", out)
	}
	if transport.calls != 1 {
		t.Fatalf("calls = %d", transport.calls)
	}
	if got := transport.last.URL.String(); got != "https://api.example.com/v1/custom" {
		t.Fatalf("URL = %q", got)
	}
	if got := transport.last.Header.Get("X-Default"); got != "yes" {
		t.Fatalf("X-Default = %q", got)
	}
}

func TestWithTransportDoesNotModifyTransport(t *testing.T) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 7
	transport.MaxIdleConnsPerHost = 3
	originalDial := reflect.ValueOf(transport.DialContext).Pointer()

	client := req.New(req.WithTransport(transport))
	if got := client.HTTPClient().Transport; got != transport {
		t.Fatalf("transport = %T, want supplied *http.Transport", got)
	}
	if transport.MaxIdleConns != 7 || transport.MaxIdleConnsPerHost != 3 {
		t.Fatalf("transport settings changed: %d, %d", transport.MaxIdleConns, transport.MaxIdleConnsPerHost)
	}
	if reflect.ValueOf(transport.DialContext).Pointer() != originalDial {
		t.Fatal("transport DialContext changed")
	}
}

func TestWithDialContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "dialed")
	}))
	defer srv.Close()

	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t.Skip("http.DefaultTransport is not *http.Transport")
	}
	defaultDial := reflect.ValueOf(defaultTransport.DialContext).Pointer()

	var calls atomic.Int32
	client := req.New(
		req.WithBaseURL(srv.URL),
		req.WithDialContext(func(ctx context.Context, network, address string) (net.Conn, error) {
			calls.Add(1)
			return (&net.Dialer{}).DialContext(ctx, network, address)
		}),
	)
	body, err := client.Get(context.Background(), "/dial").ToString()
	if err != nil {
		t.Fatalf("ToString: %v", err)
	}
	if body != "dialed" {
		t.Fatalf("body = %q", body)
	}
	if calls.Load() == 0 {
		t.Fatal("custom dial function was not called")
	}
	if reflect.ValueOf(defaultTransport.DialContext).Pointer() != defaultDial {
		t.Fatal("http.DefaultTransport.DialContext was modified")
	}
	clientTransport, ok := client.HTTPClient().Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T", client.HTTPClient().Transport)
	}
	if clientTransport == defaultTransport {
		t.Fatal("client shares http.DefaultTransport")
	}
}

func TestWithHTTPClientPreservesBehavior(t *testing.T) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	var redirects atomic.Int32
	custom := &http.Client{
		Timeout: 2 * time.Second,
		Jar:     jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			redirects.Add(1)
			return nil
		},
	}

	var finalCookie string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "abc", Path: "/"})
			http.Redirect(w, r, "/final", http.StatusFound)
		case "/final":
			cookie, err := r.Cookie("session")
			if err == nil {
				finalCookie = cookie.Value
			}
			_, _ = io.WriteString(w, "done")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := req.New(
		req.WithHTTPClient(custom),
		req.WithBaseURL(srv.URL),
		req.WithHeader("X-Builder", "yes"),
	)
	if client.HTTPClient() != custom {
		t.Fatal("HTTPClient is not the supplied pointer")
	}
	body, err := client.Get(context.Background(), "/start").ToString()
	if err != nil {
		t.Fatalf("ToString: %v", err)
	}
	if body != "done" {
		t.Fatalf("body = %q", body)
	}
	if finalCookie != "abc" {
		t.Fatalf("jar cookie = %q", finalCookie)
	}
	if redirects.Load() != 1 {
		t.Fatalf("redirects = %d", redirects.Load())
	}
	if custom.Timeout != 2*time.Second {
		t.Fatalf("custom Timeout changed to %v", custom.Timeout)
	}
}

func TestHTTPClientAccessorForDefaultClient(t *testing.T) {
	client := req.New()
	hc := client.HTTPClient()
	if hc == nil {
		t.Fatal("HTTPClient returned nil")
	}
	if hc == http.DefaultClient {
		t.Fatal("HTTPClient returned http.DefaultClient")
	}
	client.CloseIdleConnections()
}

func TestCustomRoundTripperResponseStatus(t *testing.T) {
	transport := &recordingTransport{status: http.StatusTeapot, response: "short and stout"}
	client := req.New(req.WithTransport(transport))
	_, err := client.Get(context.Background(), "https://example.com").ToBytes()
	var statusErr *req.ResponseError
	if !errors.As(err, &statusErr) {
		t.Fatalf("err = %v, want ResponseError", err)
	}
	if statusErr.StatusCode != http.StatusTeapot {
		t.Fatalf("status = %d", statusErr.StatusCode)
	}
}
