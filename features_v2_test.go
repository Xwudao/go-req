package req_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Xwudao/go-req"
)

func echoPathServer(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.URL.EscapedPath()+"?"+r.URL.RawQuery)
	}))
	t.Cleanup(s.Close)
	return s
}

func TestPathParams(t *testing.T) {
	ctx := context.Background()
	s := echoPathServer(t)
	client := req.New(req.WithBaseURL(s.URL + "/api"))

	got, err := client.Get(ctx, "/users/{id}/posts/{pid}").
		PathParam("id", "42").
		PathParam("pid", "a/b c").
		ToString()
	if err != nil || got != "/api/users/42/posts/a%2Fb%20c?" {
		t.Fatalf("got %q, %v", got, err)
	}

	got, err = client.Get(ctx, "/users/{id}/posts/{pid}?q={raw}").
		PathParams(map[string]string{"id": "1", "pid": "2"}).
		ToString()
	if err != nil || got != "/api/users/1/posts/2?q={raw}" {
		t.Fatalf("map got %q, %v", got, err)
	}

	// Without path parameters, braces are left to URL parsing as before.
	if _, err := client.Get(ctx, "/plain").ToString(); err != nil {
		t.Fatal(err)
	}

	bad := []*req.Request{
		client.Get(ctx, "/users/{id}/{other}").PathParam("id", "1"),
		client.Get(ctx, "/users/{id}").PathParam("id", "1").PathParam("extra", "x"),
		client.Get(ctx, "/users/{id}").PathParam("id", ""),
		client.Get(ctx, "/users/{id}").PathParam("id", ".."),
		client.Get(ctx, "/users/{id").PathParam("id", "1"),
		client.Get(ctx, "/users/{id}").PathParam("", "1"),
	}
	for i, r := range bad {
		if _, err := r.Build(); err == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
}

func TestWithQuery(t *testing.T) {
	ctx := context.Background()
	s := echoPathServer(t)
	client := req.New(req.WithBaseURL(s.URL), req.WithQuery("appid", "a1"), req.WithQuery("lang", "zh"))

	cases := []struct {
		req  *req.Request
		want string
	}{
		{client.Get(ctx, "/x"), "/x?appid=a1&lang=zh"},
		{client.Get(ctx, "/x?lang=en"), "/x?appid=a1&lang=en"},
		{client.Get(ctx, "/x").Query("appid", "a2"), "/x?appid=a2&lang=zh"},
		{client.Get(ctx, "/x").AddQuery("lang", "en"), "/x?appid=a1&lang=zh&lang=en"},
	}
	for i, c := range cases {
		got, err := c.req.ToString()
		if err != nil || got != c.want {
			t.Errorf("case %d: got %q, %v; want %q", i, got, err, c.want)
		}
	}
}

func TestBeforeRequest(t *testing.T) {
	ctx := context.Background()
	var gotSign, gotOrder, gotBody string
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		gotSign = r.Header.Get("X-Sign")
		gotOrder = r.Header.Get("X-Order")
		data, _ := io.ReadAll(r.Body)
		gotBody = string(data)
	}))
	defer s.Close()

	sign := func(r *http.Request) error {
		body := ""
		if r.GetBody != nil {
			rc, err := r.GetBody()
			if err != nil {
				return err
			}
			data, _ := io.ReadAll(rc)
			body = string(data)
		}
		r.Header.Set("X-Sign", r.Method+" "+r.URL.Path+" "+body)
		r.Header.Add("X-Order", "client")
		return nil
	}
	client := req.New(req.WithBaseURL(s.URL), req.WithBeforeRequest(sign))

	_, err := client.Post(ctx, "/orders").
		JSON(map[string]int{"n": 1}).
		BeforeRequest(func(r *http.Request) error {
			r.Header.Set("X-Order", r.Header.Get("X-Order")+",request")
			return nil
		}).
		ToBytes()
	if err != nil {
		t.Fatal(err)
	}
	// The body is still sent intact after the hook read it via GetBody.
	if gotSign != `POST /orders {"n":1}` || gotOrder != "client,request" || gotBody != `{"n":1}` {
		t.Fatalf("sign = %q, order = %q, body = %q", gotSign, gotOrder, gotBody)
	}

	sentinel := errors.New("no token")
	_, err = client.Get(ctx, "/x").BeforeRequest(func(*http.Request) error { return sentinel }).ToBytes()
	if !errors.Is(err, sentinel) || calls != 1 {
		t.Fatalf("err = %v, calls = %d", err, calls)
	}

	// Client.Do with a native request skips hooks.
	raw, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	resp, err := client.Do(raw)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if gotSign != "" {
		t.Fatalf("hook applied to Client.Do: %q", gotSign)
	}

	if _, err := req.NewWithOptions(req.WithBeforeRequest(nil)); !errors.Is(err, req.ErrConfiguration) {
		t.Fatalf("nil hook err = %v", err)
	}
	if _, err := client.Get(ctx, "/").BeforeRequest(nil).Build(); err == nil {
		t.Fatal("nil request hook accepted")
	}
}

func TestRequestTimeout(t *testing.T) {
	ctx := context.Background()
	release := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer s.Close()
	defer close(release)
	client := req.New(req.WithBaseURL(s.URL))

	_, err := client.Get(ctx, "/slow").Timeout(50 * time.Millisecond).ToBytes()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}

	out, err := client.Get(ctx, "/fast").Timeout(time.Second).ToJSON[map[string]bool]()
	if err != nil || !out["ok"] {
		t.Fatalf("fast = %v, %v", out, err)
	}

	built, err := client.Get(ctx, "/fast").Timeout(time.Second).Build()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := built.Context().Deadline(); ok {
		t.Fatal("Build applied the request timeout")
	}
	for _, d := range []time.Duration{0, -time.Second} {
		if _, err := client.Get(ctx, "/fast").Timeout(d).Build(); err == nil {
			t.Fatalf("timeout %v accepted", d)
		}
	}
}

func TestToJSONResponse(t *testing.T) {
	ctx := context.Background()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Total-Count", "2")
		if r.URL.Path == "/limited" {
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = io.WriteString(w, `[{"id":1},{"id":2}]`)
	}))
	defer s.Close()
	client := req.New(req.WithBaseURL(s.URL))

	users, resp, err := client.Get(ctx, "/users").ToJSONResponse[[]apiUser]()
	if err != nil || len(users) != 2 {
		t.Fatalf("users = %v, %v", users, err)
	}
	if resp.StatusCode != 200 || resp.Header.Get("X-Total-Count") != "2" || string(resp.Body) != `[{"id":1},{"id":2}]` {
		t.Fatalf("resp = %+v", resp)
	}

	_, resp, err = client.Get(ctx, "/limited").ToJSONResponse[[]apiUser]()
	var respErr *req.ResponseError
	if !errors.As(err, &respErr) || resp == nil || resp.Header.Get("Retry-After") != "3" {
		t.Fatalf("resp = %+v, err = %v", resp, err)
	}

	_, resp, err = client.Get(ctx, "/x").PathParam("missing", "1").ToJSONResponse[any]()
	if err == nil || resp != nil {
		t.Fatalf("build error: resp = %+v, err = %v", resp, err)
	}
}

type trackedBody struct {
	io.Reader
	closed bool
}

func (b *trackedBody) Close() error { b.closed = true; return nil }

func streamClient(status int, contentType, body string) (*req.Client, *trackedBody) {
	tb := &trackedBody{Reader: strings.NewReader(body)}
	client := req.New(req.WithTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		h := make(http.Header)
		if contentType != "" {
			h.Set("Content-Type", contentType)
		}
		return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: h, Body: tb}, nil
	})))
	return client, tb
}

func TestToSSE(t *testing.T) {
	ctx := context.Background()
	stream := "\uFEFF: comment\n" +
		"event: greeting\nid: 1\ndata: hello\ndata: world\n\n" +
		"data: {\"n\":2}\r\n\r\n" +
		"retry: 1500\ndata:no-space\r\r" +
		"id\ndata: last"
	client, body := streamClient(200, "text/event-stream", stream)

	var events []*req.SSEEvent
	for ev, err := range client.Get(ctx, "https://example.test").ToSSE() {
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, ev)
	}
	if !body.closed {
		t.Fatal("body not closed")
	}
	want := []req.SSEEvent{
		{ID: "1", Event: "greeting", Data: "hello\nworld"},
		{ID: "1", Data: `{"n":2}`},
		{ID: "1", Data: "no-space", Retry: 1500 * time.Millisecond},
		{ID: "", Data: "last"},
	}
	if len(events) != len(want) {
		t.Fatalf("events = %+v", events)
	}
	for i := range want {
		if *events[i] != want[i] {
			t.Errorf("event %d = %+v, want %+v", i, *events[i], want[i])
		}
	}
	n, err := events[1].JSON[struct{ N int }]()
	if err != nil || n.N != 2 {
		t.Fatalf("JSON = %+v, %v", n, err)
	}
	if _, err := events[0].JSON[map[string]any](); err == nil {
		t.Fatal("expected JSON decode error")
	}

	// Breaking early closes the body.
	client, body = streamClient(200, "text/event-stream", "data: a\n\ndata: b\n\n")
	for range client.Get(ctx, "https://example.test").ToSSE() {
		break
	}
	if !body.closed {
		t.Fatal("body not closed after break")
	}
}

func TestToSSEErrors(t *testing.T) {
	ctx := context.Background()
	check := req.WithErrorCheck(apiErrorCheck())
	newClient := func(status int, ct, body string) *req.Client {
		c, _ := streamClient(status, ct, body)
		return req.New(check, req.WithTransport(c.HTTPClient().Transport))
	}

	// An error event detected by the ErrorCheck stops the stream.
	var got []string
	var apiErr apiError
	for ev, err := range newClient(200, "text/event-stream", "data: ok\n\ndata: {\"code\":5,\"msg\":\"busy\"}\n\ndata: never\n\n").Get(ctx, "https://example.test").ToSSE() {
		if err != nil {
			if !errors.As(err, &apiErr) || apiErr.Code != 5 {
				t.Fatalf("err = %v", err)
			}
			break
		}
		got = append(got, ev.Data)
	}
	if len(got) != 1 || got[0] != "ok" || apiErr.Code != 5 {
		t.Fatalf("got = %v, apiErr = %+v", got, apiErr)
	}

	// A JSON body instead of a stream is checked as a whole.
	for _, err := range newClient(200, "application/json", `{"code":7,"msg":"no"}`).Get(ctx, "https://example.test").ToSSE() {
		if !errors.As(err, &apiErr) || apiErr.Code != 7 {
			t.Fatalf("err = %v", err)
		}
	}
	for _, err := range newClient(200, "application/json", `{"ok":true}`).Get(ctx, "https://example.test").ToSSE() {
		if err == nil || !strings.Contains(err.Error(), "expected an event stream") {
			t.Fatalf("err = %v", err)
		}
	}

	var respErr *req.ResponseError
	count := 0
	for _, err := range newClient(500, "text/plain", "down").Get(ctx, "https://example.test").ToSSE() {
		count++
		if !errors.As(err, &respErr) || respErr.StatusCode != 500 {
			t.Fatalf("err = %v", err)
		}
	}
	if count != 1 {
		t.Fatalf("yielded %d times", count)
	}

	c, _ := streamClient(200, "text/event-stream", "data: "+strings.Repeat("x", 100)+"\n\n")
	for _, err := range c.Get(ctx, "https://example.test").MaxResponseBytes(50).ToSSE() {
		if !errors.Is(err, req.ErrResponseTooLarge) {
			t.Fatalf("err = %v", err)
		}
	}
}

func TestToJSONStream(t *testing.T) {
	ctx := context.Background()
	client, body := streamClient(200, "application/x-ndjson", "{\"id\":1}\n\n  \r\n{\"id\":2}\r\n{\"id\":3}")
	var ids []int
	for u, err := range client.Get(ctx, "https://example.test").ToJSONStream[apiUser]() {
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, u.ID)
	}
	if len(ids) != 3 || ids[2] != 3 || !body.closed {
		t.Fatalf("ids = %v, closed = %v", ids, body.closed)
	}

	client, body = streamClient(200, "", "{\"id\":1}\n{\"id\":2}\n")
	for range client.Get(ctx, "https://example.test").ToJSONStream[apiUser]() {
		break
	}
	if !body.closed {
		t.Fatal("body not closed after break")
	}

	client, _ = streamClient(200, "", "{\"id\":1}\n{bad\n{\"id\":3}\n")
	ids = ids[:0]
	var lastErr error
	for u, err := range client.Get(ctx, "https://example.test").ToJSONStream[apiUser]() {
		if err != nil {
			lastErr = err
			continue
		}
		ids = append(ids, u.ID)
	}
	if lastErr == nil || len(ids) != 1 {
		t.Fatalf("ids = %v, err = %v", ids, lastErr)
	}

	c, _ := streamClient(200, "", "{\"id\":1}\n{\"code\":9,\"msg\":\"x\"}\n")
	client = req.New(req.WithErrorCheck(apiErrorCheck()), req.WithTransport(c.HTTPClient().Transport))
	var apiErr apiError
	for _, err := range client.Get(ctx, "https://example.test").ToJSONStream[apiUser]() {
		if err != nil && !errors.As(err, &apiErr) {
			t.Fatalf("err = %v", err)
		}
	}
	if apiErr.Code != 9 {
		t.Fatalf("apiErr = %+v", apiErr)
	}

	client, _ = streamClient(404, "", "")
	for _, err := range client.Get(ctx, "https://example.test").ToJSONStream[apiUser]() {
		var respErr *req.ResponseError
		if !errors.As(err, &respErr) || respErr.StatusCode != 404 {
			t.Fatalf("err = %v", err)
		}
	}
}
