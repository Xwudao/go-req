package req

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type featureRoundTripFunc func(*http.Request) (*http.Response, error)

func (f featureRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type featureBody struct {
	io.Reader
	closed bool
}

func (r *featureBody) Close() error { r.closed = true; return nil }

type featureErrorReader struct{ err error }

func (r featureErrorReader) Read(p []byte) (int, error) { return 0, r.err }

func TestToWriterClosesBody(t *testing.T) {
	sentinel := errors.New("read failed")
	for _, status := range []int{200, 400} {
		for _, fail := range []bool{false, true} {
			var reader io.Reader = strings.NewReader("data")
			if fail {
				reader = featureErrorReader{sentinel}
			}
			body := &featureBody{Reader: reader}
			c := New(WithTransport(featureRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: body}, nil
			})))
			_, err := c.Get(context.Background(), "https://example.test").ToWriter(io.Discard)
			if !body.closed {
				t.Fatal("response body not closed")
			}
			if fail && !errors.Is(err, sentinel) {
				t.Fatal(err)
			}
			if status == 400 {
				var se *StatusError
				if !errors.As(err, &se) {
					t.Fatal(err)
				}
			}
		}
	}
	body := &featureBody{Reader: strings.NewReader("data")}
	c := New(WithTransport(featureRoundTripFunc(func(r *http.Request) (*http.Response, error) { return &http.Response{StatusCode: 200, Body: body}, nil })))
	_, _ = c.Get(context.Background(), "https://example.test").ToWriter(failingWriter{sentinel})
	if !body.closed {
		t.Fatal("response body not closed after writer error")
	}
}

func TestMultipartReaderErrorAndReplacement(t *testing.T) {
	sentinel := errors.New("file read failed")
	c := New()
	r, err := c.Post(context.Background(), "https://example.test").Multipart(nil, MultipartFile{FieldName: "f", FileName: "f", Reader: featureErrorReader{sentinel}}).Build()
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(r.Body)
	r.Body.Close()
	if !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	tracked := &trackedReader{Reader: strings.NewReader("file")}
	r, err = c.Post(context.Background(), "https://example.test").Multipart(nil, MultipartFile{FieldName: "f", Reader: tracked}).Body(strings.NewReader("replacement")).Build()
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil || string(data) != "replacement" || tracked.reads != 0 || tracked.closed {
		t.Fatalf("%s %v", data, err)
	}
}
