package req_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Xwudao/go-req"
)

func TestDebugDisabledProducesNoOutput(t *testing.T) {
	var debug bytes.Buffer
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	client := req.New(
		req.WithDebug(false),
		req.WithDebugWriter(&debug),
		req.WithBaseURL(srv.URL),
	)
	if _, err := client.Get(context.Background(), "/").ToString(); err != nil {
		t.Fatalf("ToString: %v", err)
	}
	if debug.Len() != 0 {
		t.Fatalf("debug output = %q, want none", debug.String())
	}
}

func TestDebugRequestAndResponse(t *testing.T) {
	var debug bytes.Buffer
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Response", "yes")
		_, _ = io.WriteString(w, `{"id":7}`)
	}))
	defer srv.Close()

	client := req.New(
		req.WithDebug(true),
		req.WithDebugWriter(&debug),
		req.WithBaseURL(srv.URL),
		req.WithHeader("X-Client", "yes"),
	)
	ctx := context.Background()
	var out struct {
		ID int `json:"id"`
	}
	if err := client.Post(ctx, "/users?page=1").
		Header("X-Request", "yes").
		JSON(map[string]string{"name": "Alice"}).
		ToJSON(&out); err != nil {
		t.Fatalf("ToJSON: %v", err)
	}
	if out.ID != 7 {
		t.Fatalf("out = %+v", out)
	}

	output := debug.String()
	for _, want := range []string{
		"===== REQUEST =====",
		"POST ",
		"/users",
		"page=1",
		"X-Client: yes",
		"X-Request: yes",
		"Body (application/json, 16 bytes):",
		`{"name":"Alice"}`,
		"===== RESPONSE =====",
		"Status: 200 OK",
		"X-Response: yes",
		"Duration:",
		"Body (application/json, 8 bytes):",
		`{"id":7}`,
	} {
		if !strings.Contains(output, want) {
			t.Errorf("debug output missing %q\n%s", want, output)
		}
	}
}

func TestDebugNativeClientDo(t *testing.T) {
	var debug bytes.Buffer
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "native")
	}))
	defer srv.Close()

	client := req.New(
		req.WithDebug(true),
		req.WithDebugWriter(&debug),
	)
	raw, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/native", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	resp, err := client.Do(raw)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close body: %v", err)
	}
	if string(body) != "native" {
		t.Fatalf("body = %q", body)
	}

	output := debug.String()
	for _, want := range []string{"GET ", "/native", "Status: 200 OK", "Body (text/plain; charset=utf-8, 6 bytes):", "native"} {
		if !strings.Contains(output, want) {
			t.Errorf("debug output missing %q\n%s", want, output)
		}
	}
}

func TestDebugBuilderOverride(t *testing.T) {
	var debug bytes.Buffer
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()
	ctx := context.Background()

	client := req.New(req.WithDebugWriter(&debug), req.WithBaseURL(srv.URL))
	if _, err := client.Get(ctx, "/enabled").Debug(true).ToString(); err != nil {
		t.Fatalf("debug enabled request: %v", err)
	}
	if !strings.Contains(debug.String(), "/enabled") {
		t.Fatalf("builder Debug(true) produced no output: %q", debug.String())
	}
	debug.Reset()
	if _, err := client.Get(ctx, "/disabled").ToString(); err != nil {
		t.Fatalf("debug disabled request: %v", err)
	}
	if debug.Len() != 0 {
		t.Fatalf("request without override produced output: %q", debug.String())
	}

	// The override survives Build and a subsequent native Client.Do.
	built, err := client.Get(ctx, "/built").Debug(true).Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	resp, err := client.Do(built)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(debug.String(), "/built") {
		t.Fatalf("builder Debug override was lost through Build: %q", debug.String())
	}
}

func TestDebugBuilderCanDisableClientDebug(t *testing.T) {
	var debug bytes.Buffer
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	client := req.New(
		req.WithDebug(true),
		req.WithDebugWriter(&debug),
		req.WithBaseURL(srv.URL),
	)
	if _, err := client.Get(context.Background(), "/quiet").Debug(false).ToString(); err != nil {
		t.Fatalf("ToString: %v", err)
	}
	if debug.Len() != 0 {
		t.Fatalf("builder Debug(false) produced output: %q", debug.String())
	}
}

func TestDebugRedactsSensitiveData(t *testing.T) {
	var debug bytes.Buffer
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Set-Cookie", "response=RESPONSECOOKIESECRET; Path=/")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	base.User = url.UserPassword("USERNAME", "URLPASSWORDSECRET")

	client := req.New(
		req.WithDebug(true),
		req.WithDebugWriter(&debug),
		req.WithBaseURL(base.String()),
	)
	_, err = client.Get(context.Background(),
		"/secure?token=QUERYSECRET&api_key=APIKEYSECRET&page=1#access_token=FRAGMENTSECRET").
		Header("Authorization", "Bearer AUTHORIZATIONSECRET").
		Header("Proxy-Authorization", "Basic PROXYSECRET").
		Header("Cookie", "session=COOKIESECRET").
		Header("X-Api-Key", "XAPIKEYSECRET").
		Header("X-Public", "visible").
		ToBytes()
	if err != nil {
		t.Fatalf("ToBytes: %v", err)
	}

	output := debug.String()
	for _, secret := range []string{
		"AUTHORIZATIONSECRET",
		"PROXYSECRET",
		"COOKIESECRET",
		"XAPIKEYSECRET",
		"QUERYSECRET",
		"APIKEYSECRET",
		"FRAGMENTSECRET",
		"USERNAME",
		"URLPASSWORDSECRET",
		"RESPONSECOOKIESECRET",
	} {
		if strings.Contains(output, secret) {
			t.Errorf("debug output leaked %q\n%s", secret, output)
		}
	}
	for _, want := range []string{
		"Authorization: [REDACTED]",
		"Proxy-Authorization: [REDACTED]",
		"Cookie: [REDACTED]",
		"Set-Cookie: [REDACTED]",
		"X-Public: visible",
		"token=REDACTED",
		"api_key=REDACTED",
		"page=1",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("debug output missing %q\n%s", want, output)
		}
	}
}

func TestDebugBodyTruncation(t *testing.T) {
	t.Run("response", func(t *testing.T) {
		const body = "0123456789ABCDEFGHIJ"
		var debug bytes.Buffer
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, body)
		}))
		defer srv.Close()

		client := req.New(
			req.WithDebug(true),
			req.WithDebugWriter(&debug),
			req.WithDebugMaxBodySize(10),
			req.WithBaseURL(srv.URL),
		)
		got, err := client.Get(context.Background(), "/").ToString()
		if err != nil {
			t.Fatalf("ToString: %v", err)
		}
		if got != body {
			t.Fatalf("ToString truncated the HTTP body: %q", got)
		}
		output := debug.String()
		if !strings.Contains(output, "Body (text/plain, 20 bytes):") {
			t.Errorf("missing body size\n%s", output)
		}
		if !strings.Contains(output, "0123456789") {
			t.Errorf("missing captured prefix\n%s", output)
		}
		if strings.Contains(output, "ABCDEFGHIJ") {
			t.Errorf("output contains bytes beyond the debug limit\n%s", output)
		}
		if !strings.Contains(output, "[truncated: captured 10 of 20 bytes read]") {
			t.Errorf("missing truncation marker\n%s", output)
		}
	})

	t.Run("request", func(t *testing.T) {
		var debug bytes.Buffer
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			_, _ = io.WriteString(w, "ok")
		}))
		defer srv.Close()

		client := req.New(
			req.WithDebug(true),
			req.WithDebugWriter(&debug),
			req.WithDebugMaxBodySize(10),
			req.WithBaseURL(srv.URL),
		)
		_, err := client.Post(context.Background(), "/").
			Body(strings.NewReader("0123456789ABCDEFGHIJ")).
			ToString()
		if err != nil {
			t.Fatalf("ToString: %v", err)
		}
		output := debug.String()
		if !strings.Contains(output, "[truncated: captured 10 of 20 bytes read]") {
			t.Errorf("request body was not truncated\n%s", output)
		}
		if strings.Contains(output, "ABCDEFGHIJ") {
			t.Errorf("request output contains bytes beyond the debug limit\n%s", output)
		}
	})
}

func TestDebugBinaryBody(t *testing.T) {
	binaryBody := []byte{0, 1, 2, 3, 0xfe, 0xff}
	var debug bytes.Buffer
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestBody, _ := io.ReadAll(r.Body)
		if !bytes.Equal(requestBody, binaryBody) {
			t.Errorf("request body = %v", requestBody)
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(binaryBody)
	}))
	defer srv.Close()

	client := req.New(
		req.WithDebug(true),
		req.WithDebugWriter(&debug),
		req.WithBaseURL(srv.URL),
	)
	got, err := client.Post(context.Background(), "/binary").
		Header("Content-Type", "application/octet-stream").
		Body(bytes.NewReader(binaryBody)).
		ToBytes()
	if err != nil {
		t.Fatalf("ToBytes: %v", err)
	}
	if !bytes.Equal(got, binaryBody) {
		t.Fatalf("response body = %v", got)
	}

	output := debug.String()
	if strings.ContainsRune(output, 0) || strings.Contains(output, "\xfe") || strings.Contains(output, "\xff") {
		t.Fatalf("binary data was written to debug output")
	}
	for _, want := range []string{
		"Body (application/octet-stream, 6 bytes):",
		"[binary content omitted]",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("debug output missing %q\n%s", want, output)
		}
	}
}

func TestDebugStreamingResponseLogsOnlyReadBytes(t *testing.T) {
	const body = "PREFIX-SECRET-SUFFIX"
	var debug bytes.Buffer
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	client := req.New(
		req.WithDebug(true),
		req.WithDebugWriter(&debug),
		req.WithBaseURL(srv.URL),
	)
	resp, err := client.Get(context.Background(), "/stream").Do()
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	prefix := make([]byte, len("PREFIX"))
	if _, err := io.ReadFull(resp.Body, prefix); err != nil {
		t.Fatalf("read prefix: %v", err)
	}
	if string(prefix) != "PREFIX" {
		t.Fatalf("prefix = %q", prefix)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	output := debug.String()
	if !strings.Contains(output, "PREFIX") {
		t.Errorf("read bytes missing from debug output\n%s", output)
	}
	if strings.Contains(output, "SECRET") || strings.Contains(output, "SUFFIX") {
		t.Errorf("debug read beyond what the caller consumed\n%s", output)
	}
	if !strings.Contains(output, "[body not fully read: 6 bytes read]") {
		t.Errorf("missing partial-body marker\n%s", output)
	}
}

type readCountingReader struct {
	reader io.Reader
	reads  atomic.Int32
}

func (r *readCountingReader) Read(p []byte) (int, error) {
	r.reads.Add(1)
	return r.reader.Read(p)
}

func TestDebugDoesNotPreconsumeRequestBody(t *testing.T) {
	var debug bytes.Buffer
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	reader := &readCountingReader{reader: strings.NewReader("streamed request")}
	client := req.New(
		req.WithDebug(true),
		req.WithDebugWriter(&debug),
		req.WithBaseURL(srv.URL),
	)
	builder := client.Post(context.Background(), "/upload").Body(reader)
	built, err := builder.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if built == nil {
		t.Fatal("Build returned nil request")
	}
	if reads := reader.reads.Load(); reads != 0 {
		t.Fatalf("Build read the request body %d times", reads)
	}
	if debug.Len() != 0 {
		t.Fatalf("Build produced debug output: %q", debug.String())
	}

	if _, err := builder.ToString(); err != nil {
		t.Fatalf("ToString: %v", err)
	}
	if reader.reads.Load() == 0 {
		t.Fatal("request body was never read by the HTTP transport")
	}
	if !strings.Contains(debug.String(), "streamed request") {
		t.Fatalf("request body missing from debug output\n%s", debug.String())
	}
}

func TestDebugBodyDisabled(t *testing.T) {
	var debug bytes.Buffer
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, "RESPONSESECRET")
	}))
	defer srv.Close()

	client := req.New(
		req.WithDebug(true),
		req.WithDebugBody(false),
		req.WithDebugWriter(&debug),
		req.WithBaseURL(srv.URL),
	)
	_, err := client.Post(context.Background(), "/").
		Body(strings.NewReader("REQUESTSECRET")).
		ToString()
	if err != nil {
		t.Fatalf("ToString: %v", err)
	}
	output := debug.String()
	if !strings.Contains(output, "Body: [disabled]") {
		t.Errorf("missing disabled marker\n%s", output)
	}
	if strings.Contains(output, "REQUESTSECRET") || strings.Contains(output, "RESPONSESECRET") {
		t.Errorf("body logged while disabled\n%s", output)
	}
}

func TestDebugWithCustomRoundTripper(t *testing.T) {
	var debug bytes.Buffer
	var calls atomic.Int32
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{
			StatusCode: http.StatusCreated,
			Status:     "201 Created",
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"custom":true}`)),
			Request:    r,
		}, nil
	})
	client := req.New(
		req.WithDebug(true),
		req.WithDebugWriter(&debug),
		req.WithTransport(transport),
	)
	got, err := client.Get(context.Background(), "https://example.com/custom").ToString()
	if err != nil {
		t.Fatalf("ToString: %v", err)
	}
	if got != `{"custom":true}` {
		t.Fatalf("body = %q", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("custom RoundTripper calls = %d", calls.Load())
	}
	output := debug.String()
	if !strings.Contains(output, "Status: 201 Created") || !strings.Contains(output, `{"custom":true}`) {
		t.Fatalf("missing custom transport debug output\n%s", output)
	}
}

type concurrencyCheckingWriter struct {
	active   atomic.Int32
	overlaps atomic.Bool
	mu       sync.Mutex
	buf      bytes.Buffer
}

func (w *concurrencyCheckingWriter) Write(p []byte) (int, error) {
	if w.active.Add(1) != 1 {
		w.overlaps.Store(true)
	}
	defer w.active.Add(-1)
	time.Sleep(time.Millisecond)
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *concurrencyCheckingWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func TestDebugConcurrentRequests(t *testing.T) {
	const requests = 20
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "response-%s", r.URL.Query().Get("id"))
	}))
	defer srv.Close()

	writer := &concurrencyCheckingWriter{}
	client := req.New(
		req.WithDebug(true),
		req.WithDebugWriter(writer),
		req.WithBaseURL(srv.URL),
	)

	var wg sync.WaitGroup
	errs := make(chan error, requests)
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("%d", i)
			body, err := client.Get(context.Background(), "/work").
				Query("id", id).
				ToString()
			if err != nil {
				errs <- err
				return
			}
			if body != "response-"+id {
				errs <- fmt.Errorf("body = %q, want response-%s", body, id)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if writer.overlaps.Load() {
		t.Fatal("debug writes overlapped")
	}
	output := writer.String()
	for i := 0; i < requests; i++ {
		if !strings.Contains(output, fmt.Sprintf("id=%d", i)) {
			t.Errorf("missing concurrent debug output for id=%d", i)
		}
	}
}

var errDebugWriter = errors.New("debug writer failure")

type failingDebugWriter struct{}

func (failingDebugWriter) Write([]byte) (int, error) {
	return 0, errDebugWriter
}

func TestDebugWriterErrorDoesNotAffectRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	client := req.New(
		req.WithDebug(true),
		req.WithDebugWriter(failingDebugWriter{}),
		req.WithBaseURL(srv.URL),
	)
	var out struct {
		OK bool `json:"ok"`
	}
	if err := client.Get(context.Background(), "/").ToJSON(&out); err != nil {
		t.Fatalf("ToJSON: %v", err)
	}
	if !out.OK {
		t.Fatalf("out = %+v", out)
	}
	if err := client.DebugError(); !errors.Is(err, errDebugWriter) {
		t.Fatalf("DebugError = %v, want writer failure", err)
	}
}

func TestDebugConfigValidation(t *testing.T) {
	tests := []struct {
		name string
		opt  req.Option
	}{
		{name: "nil writer", opt: req.WithDebugWriter(nil)},
		{name: "zero max body", opt: req.WithDebugMaxBodySize(0)},
		{name: "negative max body", opt: req.WithDebugMaxBodySize(-1)},
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
