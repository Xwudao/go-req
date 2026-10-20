package req_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Xwudao/go-req"
)

func TestStatusError(t *testing.T) {
	tests := []int{http.StatusBadRequest, http.StatusNotFound, http.StatusInternalServerError}
	for _, status := range tests {
		status := status
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Error", "yes")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"error":"failed"}`)
			}))
			defer srv.Close()

			client := req.New(req.WithBaseURL(srv.URL))
			var result map[string]any
			err := client.Get(context.Background(), "/error").ToJSON(&result)
			if err == nil {
				t.Fatal("expected error")
			}
			var statusErr *req.StatusError
			if !errors.As(err, &statusErr) {
				t.Fatalf("err = %T, want *StatusError", err)
			}
			if statusErr.StatusCode != status {
				t.Errorf("StatusCode = %d, want %d", statusErr.StatusCode, status)
			}
			if statusErr.Status != http.StatusText(status) && !strings.Contains(statusErr.Status, http.StatusText(status)) {
				t.Errorf("Status = %q", statusErr.Status)
			}
			if got := statusErr.Header.Get("X-Error"); got != "yes" {
				t.Errorf("X-Error = %q", got)
			}
			if string(statusErr.Body) != `{"error":"failed"}` {
				t.Errorf("Body = %q", statusErr.Body)
			}
			if !strings.Contains(err.Error(), "status") {
				t.Errorf("Error() = %q", err.Error())
			}
		})
	}
}

func TestStatusErrorBodyIsLimited(t *testing.T) {
	const bodySize = 100 << 10
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, strings.Repeat("x", bodySize))
	}))
	defer srv.Close()

	client := req.New(req.WithBaseURL(srv.URL))
	_, err := client.Get(context.Background(), "/error").ToBytes()
	var statusErr *req.StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("err = %v, want *StatusError", err)
	}
	if len(statusErr.Body) != 64<<10 {
		t.Fatalf("Body length = %d, want %d", len(statusErr.Body), 64<<10)
	}
}

func TestSuccessfulResponseLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "123456")
	}))
	defer srv.Close()
	client := req.New(req.WithBaseURL(srv.URL), req.WithMaxResponseBytes(5))
	ctx := context.Background()

	if data, err := client.Get(ctx, "/").ToBytes(); err == nil || data != nil {
		t.Fatalf("ToBytes = (%q, %v), want nil error", data, err)
	} else if !errors.Is(err, req.ErrResponseTooLarge) {
		t.Fatalf("err = %v, want ErrResponseTooLarge", err)
	}

	if got, err := client.Get(ctx, "/").ToString(); err == nil || got != "" {
		t.Fatalf("ToString = (%q, %v)", got, err)
	} else if !errors.Is(err, req.ErrResponseTooLarge) {
		t.Fatalf("err = %v, want ErrResponseTooLarge", err)
	}

	var out any
	if err := client.Get(ctx, "/").ToJSON(&out); !errors.Is(err, req.ErrResponseTooLarge) {
		t.Fatalf("ToJSON err = %v, want ErrResponseTooLarge", err)
	}
}

func TestSuccessfulResponseAtLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "123456")
	}))
	defer srv.Close()
	client := req.New(req.WithBaseURL(srv.URL), req.WithMaxResponseBytes(6))
	data, err := client.Get(context.Background(), "/").ToBytes()
	if err != nil {
		t.Fatalf("ToBytes: %v", err)
	}
	if string(data) != "123456" {
		t.Fatalf("data = %q", data)
	}
}

func TestToJSONEmptyBody(t *testing.T) {
	t.Run("204", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}))
		defer srv.Close()
		client := req.New(req.WithBaseURL(srv.URL))
		value := struct {
			Name string `json:"name"`
		}{Name: "unchanged"}
		if err := client.Get(context.Background(), "/").ToJSON(&value); err != nil {
			t.Fatalf("ToJSON: %v", err)
		}
		if value.Name != "unchanged" {
			t.Fatalf("value = %+v", value)
		}
	})

	t.Run("200 empty", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()
		client := req.New(req.WithBaseURL(srv.URL))
		var value any
		if err := client.Get(context.Background(), "/").ToJSON(&value); err != nil {
			t.Fatalf("ToJSON: %v", err)
		}
		if value != nil {
			t.Fatalf("value = %#v", value)
		}
	})
}

func TestToJSONNull(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "null")
	}))
	defer srv.Close()
	client := req.New(req.WithBaseURL(srv.URL))

	value := &struct {
		Name string `json:"name"`
	}{Name: "set"}
	if err := client.Get(context.Background(), "/").ToJSON(&value); err != nil {
		t.Fatalf("ToJSON: %v", err)
	}
	if value != nil {
		t.Fatalf("value = %+v, want nil", value)
	}
}

func TestToJSONDecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "{not json")
	}))
	defer srv.Close()

	client := req.New(req.WithBaseURL(srv.URL))
	var value any
	err := client.Get(context.Background(), "/").ToJSON(&value)
	if err == nil {
		t.Fatal("expected decode error")
	}
	var syntaxErr *json.SyntaxError
	if !errors.As(err, &syntaxErr) {
		t.Fatalf("err = %T, want *json.SyntaxError", err)
	}
}

func TestToJSONNilTarget(t *testing.T) {
	if err := req.New().Get(context.Background(), "https://example.com").ToJSON(nil); err == nil {
		t.Fatal("expected error for nil target")
	}
}

type trackingReadCloser struct {
	reader io.Reader
	closed bool
}

func (r *trackingReadCloser) Read(p []byte) (int, error) {
	return r.reader.Read(p)
}

func (r *trackingReadCloser) Close() error {
	r.closed = true
	return nil
}

func TestDoDoesNotCloseBody(t *testing.T) {
	body := &trackingReadCloser{reader: strings.NewReader("stream")}
	client := req.New(req.WithTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Status:        http.StatusText(http.StatusOK),
			Header:        make(http.Header),
			Body:          body,
			ContentLength: 6,
		}, nil
	})))

	resp, err := client.Get(context.Background(), "https://example.com").Do()
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if body.closed {
		t.Fatal("Do closed the response body")
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "stream" {
		t.Fatalf("body = %q", data)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !body.closed {
		t.Fatal("explicit Close did not close the body")
	}
}

func TestResponseHelpersCloseBody(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		call   func(*req.Request) error
	}{
		{
			name:   "ToBytes success",
			status: http.StatusOK,
			body:   "ok",
			call: func(r *req.Request) error {
				_, err := r.ToBytes()
				return err
			},
		},
		{
			name:   "ToString success",
			status: http.StatusOK,
			body:   "ok",
			call: func(r *req.Request) error {
				_, err := r.ToString()
				return err
			},
		},
		{
			name:   "ToJSON success",
			status: http.StatusOK,
			body:   `{"ok":true}`,
			call: func(r *req.Request) error {
				var v any
				return r.ToJSON(&v)
			},
		},
		{
			name:   "ToJSON decode error",
			status: http.StatusOK,
			body:   `{bad`,
			call: func(r *req.Request) error {
				var v any
				return r.ToJSON(&v)
			},
		},
		{
			name:   "ToBytes status error",
			status: http.StatusInternalServerError,
			body:   "boom",
			call: func(r *req.Request) error {
				_, err := r.ToBytes()
				return err
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := &trackingReadCloser{reader: strings.NewReader(tc.body)}
			client := req.New(req.WithTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: tc.status,
					Status:     http.StatusText(tc.status),
					Header:     make(http.Header),
					Body:       body,
				}, nil
			})))
			_ = tc.call(client.Get(context.Background(), "https://example.com"))
			if !body.closed {
				t.Fatal("response body was not closed")
			}
		})
	}
}

var errReadFailure = errors.New("read failure")

type errorReadCloser struct {
	closed bool
}

func (r *errorReadCloser) Read(p []byte) (int, error) {
	return 0, errReadFailure
}

func (r *errorReadCloser) Close() error {
	r.closed = true
	return nil
}

func TestResponseBodyReadError(t *testing.T) {
	body := &errorReadCloser{}
	client := req.New(req.WithTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     http.StatusText(http.StatusOK),
			Header:     make(http.Header),
			Body:       body,
		}, nil
	})))
	_, err := client.Get(context.Background(), "https://example.com").ToBytes()
	if !errors.Is(err, errReadFailure) {
		t.Fatalf("err = %v, want read failure", err)
	}
	if !body.closed {
		t.Fatal("body was not closed after read error")
	}
}

func TestStatusErrorWithReadError(t *testing.T) {
	body := &errorReadCloser{}
	client := req.New(req.WithTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadGateway,
			Status:     http.StatusText(http.StatusBadGateway),
			Header:     make(http.Header),
			Body:       body,
		}, nil
	})))
	_, err := client.Get(context.Background(), "https://example.com").ToBytes()
	var statusErr *req.StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("err = %v, want StatusError", err)
	}
	if !errors.Is(err, errReadFailure) {
		t.Fatalf("err = %v, want read failure in chain", err)
	}
}

func TestLargeStreamingWithDo(t *testing.T) {
	payload := strings.Repeat("0123456789", 200_000) // 2 MB
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, payload)
	}))
	defer srv.Close()

	client := req.New(req.WithBaseURL(srv.URL), req.WithMaxResponseBytes(1))
	resp, err := client.Get(context.Background(), "/large").Do()
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	var dst strings.Builder
	n, err := io.Copy(&dst, resp.Body)
	if err != nil {
		t.Fatalf("io.Copy: %v", err)
	}
	if n != int64(len(payload)) || dst.Len() != len(payload) {
		t.Fatalf("copied %d/%d bytes", n, dst.Len())
	}
}

func TestSuccessfulStatusWithoutJSONContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, `{"value":42}`)
	}))
	defer srv.Close()
	client := req.New(req.WithBaseURL(srv.URL))
	var out struct {
		Value int `json:"value"`
	}
	if err := client.Get(context.Background(), "/").ToJSON(&out); err != nil {
		t.Fatalf("ToJSON: %v", err)
	}
	if out.Value != 42 {
		t.Fatalf("out = %+v", out)
	}
}

func TestStatusErrorForNon2xxWithoutBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()
	client := req.New(req.WithBaseURL(srv.URL))
	var out any
	err := client.Get(context.Background(), "/").ToJSON(&out)
	var statusErr *req.StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("err = %v, want *StatusError", err)
	}
	if statusErr.StatusCode != http.StatusNotModified {
		t.Fatalf("StatusCode = %d", statusErr.StatusCode)
	}
}
