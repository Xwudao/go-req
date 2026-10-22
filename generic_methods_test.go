//go:build go1.27

package req

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestDecode(t *testing.T) {
	ctx := context.Background()
	s := differentShapeServer(t)
	client := New()

	user, err := client.Get(ctx, s.URL+"/ok").Decode[testUser]()
	if err != nil {
		t.Fatal(err)
	}
	if user.ID != 7 || user.Name != "alice" {
		t.Fatalf("user = %+v", user)
	}

	ptr, err := client.Get(ctx, s.URL+"/ok").DecodePtr[testUser]()
	if err != nil {
		t.Fatal(err)
	}
	if ptr == nil || *ptr != user {
		t.Fatalf("ptr = %+v", ptr)
	}

	empty, err := client.Get(ctx, s.URL+"/empty-ok").Decode[testUser]()
	if err != nil || empty != (testUser{}) {
		t.Fatalf("empty = %+v, %v", empty, err)
	}
	emptyPtr, err := client.Get(ctx, s.URL+"/empty-ok").DecodePtr[testUser]()
	if err != nil || emptyPtr != nil {
		t.Fatalf("emptyPtr = %+v, %v", emptyPtr, err)
	}
}

func TestRequestDecodeWithError(t *testing.T) {
	ctx := context.Background()
	s := differentShapeServer(t)
	client := New()

	user, err := client.Get(ctx, s.URL+"/ok").DecodeWithError[testUser, testAPIError]()
	if err != nil {
		t.Fatal(err)
	}
	if user.ID != 7 || user.Name != "alice" {
		t.Fatalf("user = %+v", user)
	}

	_, err = client.Get(ctx, s.URL+"/fail").DecodeWithError[testUser, testAPIError]()
	var typedErr *TypedStatusError[testAPIError]
	if !errors.As(err, &typedErr) {
		t.Fatalf("missing TypedStatusError: %v", err)
	}
	if typedErr.StatusCode != http.StatusBadRequest || typedErr.Data.Code != 40001 {
		t.Fatalf("typed error = %+v", typedErr)
	}

	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("missing StatusError: %v", err)
	}
	if statusErr.StatusCode != typedErr.StatusCode {
		t.Fatalf("status = %d, typed = %d", statusErr.StatusCode, typedErr.StatusCode)
	}

	_, err = client.Get(ctx, s.URL+"/malformed-fail").DecodeWithError[testUser, testAPIError]()
	if !errors.As(err, &typedErr) || !errors.Is(err, ErrErrorBodyDecode) {
		t.Fatalf("malformed error = %v", err)
	}
	if typedErr.DecodeErr == nil || !apiErrorZero(typedErr.Data) {
		t.Fatalf("malformed typed error = %+v", typedErr)
	}
}

func ExampleRequest_Decode() {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"id":7,"name":"alice"}`)
	}))
	defer s.Close()

	user, err := New().Get(context.Background(), s.URL).Decode[testUser]()
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(user.ID, user.Name)
	// Output: 7 alice
}

func ExampleRequest_DecodeWithError() {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"code":40001,"message":"bad request"}`)
	}))
	defer s.Close()

	_, err := New().Get(context.Background(), s.URL).DecodeWithError[testUser, testAPIError]()
	var apiErr *TypedStatusError[testAPIError]
	if errors.As(err, &apiErr) {
		fmt.Println(apiErr.StatusCode, apiErr.Data.Code, apiErr.Data.Message)
	}
	// Output: 400 40001 bad request
}
