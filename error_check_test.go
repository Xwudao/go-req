package req_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Xwudao/go-req"
)

type apiUser struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type apiError struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

func (e apiError) Error() string { return fmt.Sprintf("code %d: %s", e.Code, e.Msg) }

func apiErrorCheck() req.ErrorCheck {
	return req.JSONError(func(resp *req.Response, e apiError) bool { return e.Code != 0 })
}

func apiServer(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/ok":
			_, _ = io.WriteString(w, `{"id":7,"name":"alice"}`)
		case "/biz-fail":
			w.Header().Set("X-Request-Id", "abc")
			_, _ = io.WriteString(w, `{"code":40001,"msg":"token expired"}`)
		case "/list":
			_, _ = io.WriteString(w, `[{"id":1,"name":"a"},{"id":2,"name":"b"}]`)
		case "/null":
			_, _ = io.WriteString(w, `null`)
		case "/empty-ok":
			w.WriteHeader(http.StatusNoContent)
		case "/http-fail":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"code":40100,"msg":"unauthorized"}`)
		case "/html-fail":
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, `<html>bad gateway</html>`)
		case "/empty-fail":
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func TestErrorCheckSuccess(t *testing.T) {
	ctx := context.Background()
	s := apiServer(t)
	client := req.New(req.WithBaseURL(s.URL), req.WithErrorCheck(apiErrorCheck()))

	user, err := client.Get(ctx, "/ok").ToJSON[apiUser]()
	if err != nil || user != (apiUser{ID: 7, Name: "alice"}) {
		t.Fatalf("user = %+v, %v", user, err)
	}

	// The probe decode into E ignores DisallowUnknownFields.
	user, err = client.Get(ctx, "/ok").DisallowUnknownFields(true).ToJSON[apiUser]()
	if err != nil || user.ID != 7 {
		t.Fatalf("strict user = %+v, %v", user, err)
	}

	// A body that does not fit E is left to T.
	list, err := client.Get(ctx, "/list").ToJSON[[]apiUser]()
	if err != nil || len(list) != 2 || list[1].Name != "b" {
		t.Fatalf("list = %+v, %v", list, err)
	}

	ptr, err := client.Get(ctx, "/null").ToJSON[*apiUser]()
	if err != nil || ptr != nil {
		t.Fatalf("null = %+v, %v", ptr, err)
	}
	ptr, err = client.Get(ctx, "/empty-ok").ToJSON[*apiUser]()
	if err != nil || ptr != nil {
		t.Fatalf("empty = %+v, %v", ptr, err)
	}
}

func TestErrorCheckBodyErrorWith2xx(t *testing.T) {
	ctx := context.Background()
	s := apiServer(t)
	client := req.New(req.WithBaseURL(s.URL), req.WithErrorCheck(apiErrorCheck()))

	user, err := client.Get(ctx, "/biz-fail").ToJSON[apiUser]()
	if user != (apiUser{}) {
		t.Fatalf("user should be zero, got %+v", user)
	}
	var apiErr apiError
	if !errors.As(err, &apiErr) || apiErr.Code != 40001 || apiErr.Msg != "token expired" {
		t.Fatalf("apiErr = %+v (%v)", apiErr, err)
	}
	var respErr *req.ResponseError
	if !errors.As(err, &respErr) {
		t.Fatalf("missing ResponseError: %v", err)
	}
	if respErr.StatusCode != http.StatusOK || respErr.Status != "200 OK" || respErr.Header.Get("X-Request-Id") != "abc" {
		t.Fatalf("meta = %d %q %v", respErr.StatusCode, respErr.Status, respErr.Header)
	}
	if string(respErr.Body) != `{"code":40001,"msg":"token expired"}` || respErr.BodyTruncated {
		t.Fatalf("body = %q", respErr.Body)
	}
	if got := err.Error(); got != "req: API error (HTTP 200 OK): code 40001: token expired" {
		t.Fatalf("Error() = %q", got)
	}

	if _, err := client.Get(ctx, "/biz-fail").ToBytes(); !errors.As(err, &apiErr) {
		t.Fatalf("ToBytes err = %v", err)
	}
	if _, err := client.Get(ctx, "/biz-fail").ToString(); !errors.As(err, &apiErr) {
		t.Fatalf("ToString err = %v", err)
	}

	// ToWriter streams 2xx bodies, so the check does not run for them.
	var dst bytes.Buffer
	if _, err := client.Get(ctx, "/biz-fail").ToWriter(&dst); err != nil || dst.Len() == 0 {
		t.Fatalf("ToWriter = %q, %v", dst.String(), err)
	}
}

func TestErrorCheckNon2xx(t *testing.T) {
	ctx := context.Background()
	s := apiServer(t)
	client := req.New(req.WithBaseURL(s.URL), req.WithErrorCheck(apiErrorCheck()))

	_, err := client.Get(ctx, "/http-fail").ToJSON[apiUser]()
	var apiErr apiError
	if !errors.As(err, &apiErr) || apiErr.Code != 40100 {
		t.Fatalf("apiErr = %+v (%v)", apiErr, err)
	}
	var respErr *req.ResponseError
	if !errors.As(err, &respErr) || respErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("respErr = %v", err)
	}

	var dst bytes.Buffer
	_, err = client.Get(ctx, "/http-fail").ToWriter(&dst)
	if !errors.As(err, &apiErr) || dst.Len() != 0 {
		t.Fatalf("ToWriter err = %v, wrote %q", err, dst.String())
	}

	for _, path := range []string{"/html-fail", "/empty-fail"} {
		_, err := client.Get(ctx, path).ToJSON[apiUser]()
		if !errors.As(err, &respErr) || respErr.Err != nil {
			t.Fatalf("%s: err = %v", path, err)
		}
		if errors.As(err, &apiErr) {
			t.Fatalf("%s: unexpected API error %+v", path, apiErr)
		}
		if got := err.Error(); got != "req: unexpected HTTP status: "+respErr.Status {
			t.Fatalf("%s: Error() = %q", path, got)
		}
	}
}

func TestErrorCheckPredicateSeesResponse(t *testing.T) {
	ctx := context.Background()
	s := apiServer(t)
	var seen []int
	check := req.JSONError(func(resp *req.Response, e apiError) bool {
		seen = append(seen, resp.StatusCode)
		return !resp.IsSuccess()
	})
	client := req.New(req.WithBaseURL(s.URL), req.WithErrorCheck(check))

	// The predicate decides 2xx bodies are never errors.
	if _, err := client.Get(ctx, "/biz-fail").ToJSON[apiUser](); err != nil {
		t.Fatal(err)
	}
	_, err := client.Get(ctx, "/http-fail").ToJSON[apiUser]()
	var apiErr apiError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v", err)
	}
	if len(seen) != 2 || seen[0] != 200 || seen[1] != 401 {
		t.Fatalf("seen = %v", seen)
	}
}

func TestRequestErrorCheckOverride(t *testing.T) {
	ctx := context.Background()
	s := apiServer(t)
	client := req.New(req.WithBaseURL(s.URL), req.WithErrorCheck(apiErrorCheck()))

	// nil disables the Client's check for one request.
	if _, err := client.Get(ctx, "/biz-fail").ErrorCheck(nil).ToJSON[apiUser](); err != nil {
		t.Fatal(err)
	}

	// A raw ErrorCheck may inspect anything, not only JSON.
	sentinel := errors.New("traced")
	_, err := client.Get(ctx, "/ok").ErrorCheck(func(resp *req.Response) error {
		if resp.Header.Get("Content-Type") == "application/json" {
			return sentinel
		}
		return nil
	}).ToJSON[apiUser]()
	var respErr *req.ResponseError
	if !errors.Is(err, sentinel) || !errors.As(err, &respErr) || respErr.StatusCode != 200 {
		t.Fatalf("err = %v", err)
	}

	// Without a Client check, only the status decides.
	plain := req.New(req.WithBaseURL(s.URL))
	if _, err := plain.Get(ctx, "/biz-fail").ToJSON[apiUser](); err != nil {
		t.Fatal(err)
	}
	if _, err := plain.Get(ctx, "/biz-fail").ErrorCheck(apiErrorCheck()).ToJSON[apiUser](); err == nil {
		t.Fatal("request-level check was not applied")
	}
}

func TestErrorCheckConfiguration(t *testing.T) {
	if _, err := req.NewWithOptions(req.WithErrorCheck(nil)); !errors.Is(err, req.ErrConfiguration) {
		t.Fatalf("nil check err = %v", err)
	}
	if check := req.JSONError[apiError](nil); check != nil {
		t.Fatal("JSONError(nil) should return a nil check")
	}
	if _, err := req.NewWithOptions(req.WithErrorCheck(req.JSONError[apiError](nil))); !errors.Is(err, req.ErrConfiguration) {
		t.Fatalf("JSONError(nil) err = %v", err)
	}
}
