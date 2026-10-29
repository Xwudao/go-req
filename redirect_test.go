package req_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Xwudao/go-req"
)

func redirectServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var finalHits atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/short":
			http.Redirect(w, r, "/final?x=1", http.StatusFound)
		case "/abs":
			w.Header().Set("Location", "https://example.com/elsewhere")
			w.WriteHeader(http.StatusMovedPermanently)
		case "/chain":
			http.Redirect(w, r, "/short", http.StatusFound)
		case "/no-location":
			w.WriteHeader(http.StatusFound)
		case "/final":
			finalHits.Add(1)
			_, _ = io.WriteString(w, `{"final":true}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(s.Close)
	return s, &finalHits
}

func TestToLocation(t *testing.T) {
	ctx := context.Background()
	s, hits := redirectServer(t)
	client := req.New(req.WithBaseURL(s.URL))

	loc, err := client.Get(ctx, "/short").ToLocation()
	if err != nil || loc.String() != s.URL+"/final?x=1" {
		t.Fatalf("loc = %v, %v", loc, err)
	}
	loc, err = client.Get(ctx, "/abs").ToLocation()
	if err != nil || loc.String() != "https://example.com/elsewhere" {
		t.Fatalf("abs loc = %v, %v", loc, err)
	}
	if hits.Load() != 0 {
		t.Fatalf("redirect was followed %d times", hits.Load())
	}

	if _, err := client.Get(ctx, "/no-location").ToLocation(); !errors.Is(err, http.ErrNoLocation) {
		t.Fatalf("no-location err = %v", err)
	}
	var respErr *req.ResponseError
	for path, status := range map[string]int{"/final": 200, "/missing": 404} {
		if _, err := client.Get(ctx, path).ToLocation(); !errors.As(err, &respErr) || respErr.StatusCode != status {
			t.Fatalf("%s err = %v", path, err)
		}
	}
}

func TestNoRedirect(t *testing.T) {
	ctx := context.Background()
	s, hits := redirectServer(t)
	client := req.New(req.WithBaseURL(s.URL))

	// Default: redirects are followed.
	out, err := client.Get(ctx, "/chain").ToJSON[map[string]bool]()
	if err != nil || !out["final"] || hits.Load() != 1 {
		t.Fatalf("follow = %v, %v, hits %d", out, err, hits.Load())
	}

	_, resp, err := client.Get(ctx, "/short").NoRedirect().ToJSONResponse[any]()
	var respErr *req.ResponseError
	if !errors.As(err, &respErr) || resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/final?x=1" {
		t.Fatalf("resp = %+v, err = %v", resp, err)
	}

	built, err := client.Get(ctx, "/short").NoRedirect().Build()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := client.Do(built)
	if err != nil {
		t.Fatal(err)
	}
	raw.Body.Close()
	if raw.StatusCode != http.StatusFound {
		t.Fatalf("Build+Do status = %d", raw.StatusCode)
	}

	// Works with a caller-supplied http.Client without mutating it, and with Debug.
	external := &http.Client{}
	ext := req.New(req.WithBaseURL(s.URL), req.WithHTTPClient(external), req.WithDebug(true), req.WithDebugWriter(io.Discard))
	if loc, err := ext.Get(ctx, "/short").ToLocation(); err != nil || loc.Path != "/final" {
		t.Fatalf("external loc = %v, %v", loc, err)
	}
	if external.CheckRedirect != nil {
		t.Fatal("external client was modified")
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d", hits.Load())
	}
}

func TestRedirectOptions(t *testing.T) {
	ctx := context.Background()
	s, _ := redirectServer(t)

	client := req.New(req.WithBaseURL(s.URL), req.WithoutRedirect())
	resp, err := client.Get(ctx, "/short").Do()
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	sentinel := errors.New("too many redirects")
	client = req.New(req.WithBaseURL(s.URL), req.WithCheckRedirect(func(r *http.Request, via []*http.Request) error {
		if len(via) >= 2 {
			return sentinel
		}
		return nil
	}))
	if _, err := client.Get(ctx, "/short").ToBytes(); err != nil {
		t.Fatalf("one redirect: %v", err)
	}
	if _, err := client.Get(ctx, "/chain").ToBytes(); !errors.Is(err, sentinel) {
		t.Fatalf("chain err = %v", err)
	}

	for _, opt := range []req.Option{req.WithoutRedirect(), req.WithCheckRedirect(func(*http.Request, []*http.Request) error { return nil })} {
		if _, err := req.NewWithOptions(req.WithHTTPClient(&http.Client{}), opt); !errors.Is(err, req.ErrConfiguration) {
			t.Fatalf("conflict err = %v", err)
		}
	}
	if _, err := req.NewWithOptions(req.WithCheckRedirect(nil)); !errors.Is(err, req.ErrConfiguration) {
		t.Fatalf("nil policy err = %v", err)
	}
}
