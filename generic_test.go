package req

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type testUser struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type testAPIError struct {
	Code    int            `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
}

func differentShapeServer(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/ok":
			_, _ = io.WriteString(w, `{"id":7,"name":"alice"}`)
		case "/empty-ok":
			w.WriteHeader(http.StatusNoContent)
		case "/fail":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"code":40001,"message":"bad request","details":{"field":"name"}}`)
		case "/empty-fail":
			w.WriteHeader(http.StatusServiceUnavailable)
		case "/malformed-fail":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"code":50001,`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"code":40400,"message":"not found"}`)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func apiErrorZero(e testAPIError) bool {
	return e.Code == 0 && e.Message == "" && e.Details == nil
}

func TestErrorJSONDifferentShapes(t *testing.T) {
	ctx := context.Background()
	s := differentShapeServer(t)
	client := New()

	var user testUser
	var apiErr testAPIError
	if err := client.Get(ctx, s.URL+"/ok").ErrorJSON(&apiErr).ToJSON(&user); err != nil {
		t.Fatal(err)
	}
	if user.ID != 7 || user.Name != "alice" {
		t.Fatalf("success decoded incorrectly: %+v", user)
	}
	if !apiErrorZero(apiErr) {
		t.Fatalf("error target changed on success: %+v", apiErr)
	}

	err := client.Get(ctx, s.URL+"/fail").ErrorJSON(&apiErr).ToJSON(&user)
	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("missing StatusError: %v", err)
	}
	if statusErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", statusErr.StatusCode)
	}
	if apiErr.Code != 40001 || apiErr.Message != "bad request" || apiErr.Details["field"] != "name" {
		t.Fatalf("error body not decoded: %+v", apiErr)
	}
	if user.ID != 7 || user.Name != "alice" {
		t.Fatalf("success target changed by error response: %+v", user)
	}

	var decoded testAPIError
	if err := statusErr.DecodeJSON(&decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Code != apiErr.Code || decoded.Message != apiErr.Message {
		t.Fatalf("DecodeJSON = %+v", decoded)
	}
}

func TestErrorJSONMalformedAndEmpty(t *testing.T) {
	ctx := context.Background()
	s := differentShapeServer(t)
	client := New()

	apiErr := testAPIError{Code: 1, Message: "unchanged"}
	_, err := client.Get(ctx, s.URL+"/malformed-fail").ErrorJSON(&apiErr).ToBytes()
	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("missing StatusError: %v", err)
	}
	if !errors.Is(err, ErrErrorBodyDecode) {
		t.Fatalf("missing ErrErrorBodyDecode: %v", err)
	}
	if statusErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d", statusErr.StatusCode)
	}
	if string(statusErr.Body) != `{"code":50001,` {
		t.Fatalf("raw body = %q", statusErr.Body)
	}
	if apiErr.Code != 1 || apiErr.Message != "unchanged" || apiErr.Details != nil {
		t.Fatalf("malformed body changed target: %+v", apiErr)
	}

	apiErr = testAPIError{Code: 2, Message: "still unchanged"}
	_, err = client.Get(ctx, s.URL+"/empty-fail").ErrorJSON(&apiErr).ToString()
	if !errors.As(err, &statusErr) {
		t.Fatalf("missing StatusError: %v", err)
	}
	if errors.Is(err, ErrErrorBodyDecode) {
		t.Fatalf("empty body reported as decode failure: %v", err)
	}
	if apiErr.Code != 2 || apiErr.Message != "still unchanged" || apiErr.Details != nil {
		t.Fatalf("empty body changed target: %+v", apiErr)
	}
}

func TestErrorJSONToWriterAndUseNumber(t *testing.T) {
	ctx := context.Background()
	s := differentShapeServer(t)
	client := New()

	var apiErr testAPIError
	var dst bytes.Buffer
	if _, err := client.Get(ctx, s.URL+"/fail").ErrorJSON(&apiErr).ToWriter(&dst); err == nil {
		t.Fatal("expected status error")
	} else if !errors.As(err, new(*StatusError)) {
		t.Fatalf("missing StatusError: %v", err)
	}
	if dst.Len() != 0 {
		t.Fatalf("error body written to writer: %q", dst.String())
	}
	if apiErr.Code != 40001 {
		t.Fatalf("error target not decoded by ToWriter: %+v", apiErr)
	}

	var numbers map[string]any
	_, err := client.Get(ctx, s.URL+"/fail").UseNumber(true).ErrorJSON(&numbers).ToBytes()
	if !errors.As(err, new(*StatusError)) {
		t.Fatal(err)
	}
	if _, ok := numbers["code"].(json.Number); !ok {
		t.Fatalf("UseNumber not honored for ErrorJSON: %T", numbers["code"])
	}
}

func TestErrorJSONNilTarget(t *testing.T) {
	_, err := New().Get(context.Background(), "http://example.test").ErrorJSON(nil).Build()
	if err == nil || !strings.Contains(err.Error(), "ErrorJSON target") {
		t.Fatalf("nil target accepted: %v", err)
	}
}

func TestGenericToJSON(t *testing.T) {
	ctx := context.Background()
	s := differentShapeServer(t)
	client := New()

	user, err := ToJSON[testUser](client.Get(ctx, s.URL+"/ok"))
	if err != nil {
		t.Fatal(err)
	}
	if user.ID != 7 || user.Name != "alice" {
		t.Fatalf("user = %+v", user)
	}

	userPtr, err := ToJSONPtr[testUser](client.Get(ctx, s.URL+"/ok"))
	if err != nil {
		t.Fatal(err)
	}
	if userPtr == nil || *userPtr != user {
		t.Fatalf("userPtr = %+v", userPtr)
	}

	empty, err := ToJSON[testUser](client.Get(ctx, s.URL+"/empty-ok"))
	if err != nil || empty != (testUser{}) {
		t.Fatalf("empty = %+v, %v", empty, err)
	}
	emptyPtr, err := ToJSONPtr[testUser](client.Get(ctx, s.URL+"/empty-ok"))
	if err != nil || emptyPtr != nil {
		t.Fatalf("emptyPtr = %+v, %v", emptyPtr, err)
	}
}

func TestGenericToJSONWithError(t *testing.T) {
	ctx := context.Background()
	s := differentShapeServer(t)
	client := New()

	user, err := ToJSONWithError[testUser, testAPIError](client.Get(ctx, s.URL+"/ok"))
	if err != nil {
		t.Fatal(err)
	}
	if user.ID != 7 || user.Name != "alice" {
		t.Fatalf("user = %+v", user)
	}

	_, err = ToJSONWithError[testUser, testAPIError](client.Get(ctx, s.URL+"/fail"))
	var typedErr *TypedStatusError[testAPIError]
	if !errors.As(err, &typedErr) {
		t.Fatalf("missing TypedStatusError: %v", err)
	}
	if typedErr.StatusCode != http.StatusBadRequest || typedErr.Status != "400 Bad Request" {
		t.Fatalf("typed status = %d %q", typedErr.StatusCode, typedErr.Status)
	}
	if typedErr.Data.Code != 40001 || typedErr.Data.Details["field"] != "name" {
		t.Fatalf("typed data = %+v", typedErr.Data)
	}
	if typedErr.DecodeErr != nil || typedErr.ReadErr != nil {
		t.Fatalf("unexpected typed errors: %v %v", typedErr.DecodeErr, typedErr.ReadErr)
	}

	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("TypedStatusError does not expose StatusError: %v", err)
	}
	if statusErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", statusErr.StatusCode)
	}

	var decoded testAPIError
	if err := typedErr.DecodeJSON(&decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Message != "bad request" {
		t.Fatalf("decoded = %+v", decoded)
	}
}

func TestGenericToJSONWithMalformedAndEmptyError(t *testing.T) {
	ctx := context.Background()
	s := differentShapeServer(t)
	client := New()

	_, err := ToJSONWithError[testUser, testAPIError](client.Get(ctx, s.URL+"/malformed-fail"))
	var typedErr *TypedStatusError[testAPIError]
	if !errors.As(err, &typedErr) {
		t.Fatalf("missing TypedStatusError: %v", err)
	}
	if !errors.Is(err, ErrErrorBodyDecode) || typedErr.DecodeErr == nil {
		t.Fatalf("missing decode error: %v", err)
	}
	if !apiErrorZero(typedErr.Data) {
		t.Fatalf("malformed body produced data: %+v", typedErr.Data)
	}
	if string(typedErr.Body) != `{"code":50001,` {
		t.Fatalf("raw body = %q", typedErr.Body)
	}

	_, err = ToJSONWithError[testUser, testAPIError](client.Get(ctx, s.URL+"/empty-fail"))
	if !errors.As(err, &typedErr) {
		t.Fatalf("missing TypedStatusError: %v", err)
	}
	if typedErr.StatusCode != http.StatusServiceUnavailable || typedErr.DecodeErr != nil || !apiErrorZero(typedErr.Data) {
		t.Fatalf("empty error body: %+v", typedErr)
	}
}

func TestGenericNilRequest(t *testing.T) {
	if _, err := ToJSON[testUser](nil); err == nil {
		t.Fatal("ToJSON accepted nil")
	}
	if _, err := ToJSONPtr[testUser](nil); err == nil {
		t.Fatal("ToJSONPtr accepted nil")
	}
	if _, err := ToJSONWithError[testUser, testAPIError](nil); err == nil {
		t.Fatal("ToJSONWithError accepted nil")
	}
}

func ExampleToJSON() {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"id":7,"name":"alice"}`)
	}))
	defer s.Close()

	user, err := ToJSON[testUser](New().Get(context.Background(), s.URL))
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(user.ID, user.Name)
	// Output: 7 alice
}

func ExampleToJSONWithError() {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"code":40001,"message":"bad request"}`)
	}))
	defer s.Close()

	_, err := ToJSONWithError[testUser, testAPIError](New().Get(context.Background(), s.URL))
	var apiErr *TypedStatusError[testAPIError]
	if errors.As(err, &apiErr) {
		fmt.Println(apiErr.StatusCode, apiErr.Data.Code, apiErr.Data.Message)
	}
	// Output: 400 40001 bad request
}

func ExampleRequest_ErrorJSON() {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"code":40301,"message":"forbidden"}`)
	}))
	defer s.Close()

	var apiErr testAPIError
	_, err := New().Get(context.Background(), s.URL).ErrorJSON(&apiErr).ToBytes()
	var statusErr *StatusError
	if errors.As(err, &statusErr) {
		fmt.Println(statusErr.StatusCode, apiErr.Message)
	}
	// Output: 403 forbidden
}
