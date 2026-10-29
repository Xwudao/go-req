package req

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

func bodyServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(s.Close)
	return s
}

func TestResponseOptions(t *testing.T) {
	ctx := context.Background()
	s := bodyServer(t, 200, `{"n":9007199254740993,"extra":true}`)
	c := New(WithMaxResponseBytes(1))
	result, err := c.Get(ctx, s.URL).MaxResponseBytes(100).UseNumber(true).ToJSON[map[string]any]()
	if err != nil {
		t.Fatal(err)
	}
	if result["n"] != json.Number("9007199254740993") {
		t.Fatal(result)
	}
	if _, err := c.Get(ctx, s.URL).ToBytes(); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatal(err)
	}
	type target struct {
		N json.Number `json:"n"`
	}
	if _, err := c.Get(ctx, s.URL).MaxResponseBytes(100).DisallowUnknownFields(true).ToJSON[target](); err == nil {
		t.Fatal("expected unknown field error")
	}
	for _, n := range []int64{0, -1, math.MaxInt64} {
		if _, err := c.Get(ctx, s.URL).MaxResponseBytes(n).Build(); !errors.Is(err, ErrConfiguration) {
			t.Fatal(err)
		}
	}
	for _, body := range []string{`{} {}`, `{} trailing`, ` `} {
		srv := bodyServer(t, 200, body)
		if _, err := New().Get(ctx, srv.URL).ToJSON[map[string]any](); err == nil {
			t.Fatalf("accepted %q", body)
		}
	}
}

type failingWriter struct{ err error }

func (w failingWriter) Write(p []byte) (int, error) { return 0, w.err }

func TestToWriter(t *testing.T) {
	ctx := context.Background()
	c := New(WithMaxResponseBytes(1))
	s := bodyServer(t, 200, "abcdef")
	var dst bytes.Buffer
	n, err := c.Get(ctx, s.URL).MaxResponseBytes(1).ToWriter(&dst)
	if err != nil || n != 6 || dst.String() != "abcdef" {
		t.Fatalf("%d %v %s", n, err, dst.String())
	}
	sentinel := errors.New("writer failed")
	if n, err = c.Get(ctx, s.URL).ToWriter(failingWriter{sentinel}); n != 0 || !errors.Is(err, sentinel) {
		t.Fatalf("%d %v", n, err)
	}
	if _, err = c.Get(ctx, s.URL).ToWriter(nil); err == nil {
		t.Fatal("nil writer accepted")
	}
	s = bodyServer(t, 500, strings.Repeat("x", int(maxErrorBodyBytes)+1))
	dst.Reset()
	_, err = c.Get(ctx, s.URL).ToWriter(&dst)
	var se *ResponseError
	if !errors.As(err, &se) || !se.BodyTruncated || len(se.Body) != int(maxErrorBodyBytes) || dst.Len() != 0 {
		t.Fatalf("bad status error: %v", err)
	}
	_, err = c.Get(ctx, s.URL).ToBytes()
	if !errors.As(err, &se) || !se.BodyTruncated {
		t.Fatal(err)
	}
	exact := bodyServer(t, 400, strings.Repeat("x", int(maxErrorBodyBytes)))
	_, err = c.Get(ctx, exact.URL).ToBytes()
	if !errors.As(err, &se) || se.BodyTruncated {
		t.Fatal(err)
	}
}

type trackedReader struct {
	*strings.Reader
	closed bool
	reads  int
}

func (r *trackedReader) Read(p []byte) (int, error) { r.reads++; return r.Reader.Read(p) }
func (r *trackedReader) Close() error               { r.closed = true; return nil }

func TestMultipartStreaming(t *testing.T) {
	ctx := context.Background()
	file := &trackedReader{Reader: strings.NewReader("file contents")}
	fields := url.Values{"label": {"first", "second"}}
	b := New().Post(ctx, "http://example.test").Multipart(fields,
		MultipartFile{FieldName: "files", FileName: "hello.txt", Reader: file},
		MultipartFile{FieldName: "files", FileName: "second.txt", ContentType: "text/plain", Reader: strings.NewReader("second")})
	fields["label"][0] = "mutated"
	raw, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	if file.reads != 0 {
		t.Fatal("Build read the file")
	}
	if raw.GetBody != nil {
		t.Fatal("multipart must not claim replayability")
	}
	mr, err := raw.MultipartReader()
	if err != nil {
		t.Fatal(err)
	}
	var values []string
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(part)
		if err != nil {
			t.Fatal(err)
		}
		values = append(values, string(data))
	}
	if strings.Join(values, "|") != "first|second|file contents|second" {
		t.Fatal(values)
	}
	_ = raw.Body.Close()
	if file.closed {
		t.Fatal("closed caller file")
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	raw, err = New().Post(cancelCtx, "http://example.test").Multipart(nil, MultipartFile{FieldName: "f", FileName: "f", Reader: strings.NewReader("x")}).Build()
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err = io.ReadAll(raw.Body); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	_ = raw.Body.Close()
}

func TestMultipartHTTP(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mr, err := r.MultipartReader()
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		p, err := mr.NextPart()
		if err != nil {
			t.Error(err)
			return
		}
		data, err := io.ReadAll(p)
		if err != nil {
			t.Error(err)
		}
		if string(data) != "upload" || p.FileName() != "a.txt" {
			t.Error("incorrect file")
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer s.Close()
	file := &trackedReader{Reader: strings.NewReader("upload")}
	got, err := New().Post(context.Background(), s.URL).Multipart(nil, MultipartFile{FieldName: "f", FileName: "a.txt", Reader: file}).ToString()
	if err != nil || got != "ok" || file.closed {
		t.Fatalf("%s %v", got, err)
	}
	for _, f := range []MultipartFile{{FieldName: "f"}, {Reader: strings.NewReader("")}, {FieldName: "f", FileName: "a\r\nb", Reader: strings.NewReader("")}} {
		if _, err := New().Post(context.Background(), s.URL).Multipart(nil, f).Build(); err == nil {
			t.Fatal("invalid file accepted")
		}
	}
}

func TestConfiguredDebugRedaction(t *testing.T) {
	s := bodyServer(t, 200, `{"nested":[{"private":"response-secret"}],"n":9007199254740993}`)
	var logs bytes.Buffer
	c := New(WithDebug(true), WithDebugWriter(&logs), WithDebugRedactHeaders("X-Business"), WithDebugRedactQuery("business"), WithDebugRedactJSONFields("private"))
	_, err := c.Post(context.Background(), s.URL+"?business=query-secret").Header("X-Business", "header-secret").JSON(map[string]any{"private": "body-secret"}).ToBytes()
	if err != nil {
		t.Fatal(err)
	}
	out := logs.String()
	for _, secret := range []string{"response-secret", "query-secret", "header-secret", "body-secret"} {
		if strings.Contains(out, secret) {
			t.Fatalf("leaked %s", secret)
		}
	}
	for _, want := range []string{"Request-ID:", "Headers-Duration:", "Body-Duration:", "9007199254740993"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s", want)
		}
	}
	lines := strings.Split(out, "\n")
	var ids []string
	for _, line := range lines {
		if strings.HasPrefix(line, "Request-ID:") {
			ids = append(ids, line)
		}
	}
	if len(ids) != 2 || ids[0] != ids[1] {
		t.Fatal(ids)
	}
	cfg := debugConfig{redactJSON: []string{"private"}}
	snapshot := redactConfiguredBody("application/json", debugBodySnapshot{Data: []byte(`{"private":"secret`), Total: 18, Truncated: true}, cfg)
	if strings.Contains(string(snapshot.Data), "secret") {
		t.Fatal("incomplete body leaked")
	}
}

func TestDebugConcurrentIDs(t *testing.T) {
	s := bodyServer(t, 200, `{}`)
	var logs bytes.Buffer
	c := New(WithDebug(true), WithDebugWriter(&logs))
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Get(context.Background(), s.URL).ToBytes(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	counts := map[string]int{}
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.HasPrefix(line, "Request-ID:") {
			counts[line]++
		}
	}
	if len(counts) != 10 {
		t.Fatal(counts)
	}
	for _, n := range counts {
		if n != 2 {
			t.Fatal(counts)
		}
	}
}
