package req

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// Response is a buffered HTTP response passed to an ErrorCheck and embedded in
// ResponseError.
//
// For a 2xx response, Body is the complete body, bounded by the response size
// limit. For other statuses, Body contains at most 64 KiB and BodyTruncated
// reports whether more data was discarded.
type Response struct {
	StatusCode int
	Status     string
	Header     http.Header
	Body       []byte
	// BodyTruncated reports whether a non-2xx Body exceeded the capture limit.
	BodyTruncated bool
}

// IsSuccess reports whether the status code is in the 2xx range.
func (r *Response) IsSuccess() bool {
	return r.StatusCode >= 200 && r.StatusCode <= 299
}

// ErrorCheck decides whether a buffered response is an API error.
//
// It is called for every response read by ToJSON, ToBytes, and ToString, and
// for non-2xx responses read by ToWriter. Returning a non-nil error makes the
// call fail with a *ResponseError whose Err is that error, even for a 2xx
// response. Returning nil keeps the default behavior: 2xx succeeds and any
// other status fails with a *ResponseError whose Err is nil.
//
// The check must not retain or modify resp.Body after it returns.
type ErrorCheck func(resp *Response) error

// JSONError returns an ErrorCheck for APIs whose error payload is a JSON
// object of type E, possibly sent with a 2xx status.
//
// For every non-empty body that decodes into E, isError is called with the
// response and the decoded value; when it reports true, the value becomes
// ResponseError.Err and can be retrieved with errors.As. Bodies that do not
// decode into E are left to the default status handling. Decoding ignores
// DisallowUnknownFields and does not use UseNumber. A nil isError returns a nil
// ErrorCheck, which WithErrorCheck rejects.
//
// E is usually a value type with an Error method:
//
//	type APIError struct {
//	    Code int    `json:"code"`
//	    Msg  string `json:"msg"`
//	}
//
//	func (e APIError) Error() string { return fmt.Sprintf("%d: %s", e.Code, e.Msg) }
//
//	check := req.JSONError(func(resp *req.Response, e APIError) bool {
//	    return e.Code != 0
//	})
func JSONError[E error](isError func(resp *Response, e E) bool) ErrorCheck {
	if isError == nil {
		return nil
	}
	return func(resp *Response) error {
		body := bytes.TrimSpace(resp.Body)
		if len(body) == 0 || bytes.Equal(body, []byte("null")) {
			return nil
		}
		var e E
		if json.Unmarshal(body, &e) != nil {
			return nil
		}
		if !isError(resp, e) {
			return nil
		}
		return e
	}
}

// ResponseError is returned when a response is an error, either because its
// status is outside 2xx or because an ErrorCheck reported one.
//
// Err holds the error returned by the ErrorCheck and is nil for a non-2xx
// response that the check did not classify. Unwrap exposes Err, so the API
// error type can be retrieved directly with errors.As.
type ResponseError struct {
	Response
	Err error
}

// Error implements error.
func (e *ResponseError) Error() string {
	status := e.Status
	if status == "" {
		status = strconv.Itoa(e.StatusCode)
	}
	if e.Err != nil {
		return fmt.Sprintf("req: API error (HTTP %s): %v", status, e.Err)
	}
	return fmt.Sprintf("req: unexpected HTTP status: %s", status)
}

// Unwrap returns Err.
func (e *ResponseError) Unwrap() error {
	return e.Err
}

// ToJSON sends the request, requires a successful response, and decodes the
// JSON body into T. It closes the body.
//
// An empty body, including a 204 No Content response, returns the zero value
// of T without an error. Use a pointer type to distinguish it from a decoded
// zero value: ToJSON[*User] returns nil for an empty body.
//
// Errors reported by the ErrorCheck and non-2xx responses return
// *ResponseError. Network, read, and decode errors are returned with their
// original error chain intact.
//
//	user, err := client.Get(ctx, "/users/1").ToJSON[User]()
func (r *Request) ToJSON[T any]() (T, error) {
	value, _, err := r.ToJSONResponse[T]()
	return value, err
}

// ToJSONResponse is like ToJSON but also returns the buffered response, so
// headers such as pagination or rate-limit information remain available.
//
// The *Response is non-nil whenever a response was received, including when
// the status is not 2xx, the ErrorCheck reports an error, or decoding fails.
// It is nil for build, network, and body read errors. Response.Body holds the
// raw body; for a non-2xx response it is bounded to 64 KiB.
//
//	users, resp, err := client.Get(ctx, "/users").ToJSONResponse[[]User]()
//	total := resp.Header.Get("X-Total-Count")
func (r *Request) ToJSONResponse[T any]() (T, *Response, error) {
	var value T
	resp, err := r.response()
	if err != nil {
		return value, resp, err
	}
	if len(resp.Body) == 0 {
		return value, resp, nil
	}
	if err := decodeJSONResponse(resp.Body, &value, r.jsonUseNumber, r.jsonStrict); err != nil {
		return value, resp, err
	}
	return value, resp, nil
}

// ToBytes sends the request, requires a successful response, reads the
// response body, and closes it.
//
// The body is limited by DefaultMaxResponseBytes or the value supplied to
// WithMaxResponseBytes. ErrResponseTooLarge is returned when the limit is
// exceeded. For large downloads, use ToWriter or Do.
func (r *Request) ToBytes() ([]byte, error) {
	return r.responseBytes()
}

// ToString is like ToBytes but returns the body as a string.
func (r *Request) ToString() (string, error) {
	data, err := r.responseBytes()
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ToLocation sends the request without following redirects and returns the
// resolved target of the redirect response. A relative Location header is
// resolved against the request URL. The body is discarded and closed.
//
// A 3xx response without a Location header returns an error matching
// http.ErrNoLocation. Any other status returns a *ResponseError; the
// ErrorCheck runs for non-2xx responses.
//
//	loc, err := client.Get(ctx, "/short/abc").ToLocation()
//	fmt.Println(loc.String())
func (r *Request) ToLocation() (*url.URL, error) {
	if r == nil {
		return nil, errors.New("req: nil request")
	}
	resp, err := r.NoRedirect().Do()
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, errors.New("req: HTTP client returned a nil response")
	}
	defer func() {
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()
	if resp.StatusCode >= 300 && resp.StatusCode <= 399 {
		loc, err := resp.Location()
		if err != nil {
			return nil, fmt.Errorf("req: redirect location: %w", err)
		}
		return loc, nil
	}
	if !isSuccess(resp.StatusCode) {
		_, err := r.errorFromResponse(resp)
		return nil, err
	}
	body, truncated, _ := readLimited(resp.Body, maxErrorBodyBytes)
	return nil, &ResponseError{Response: *newResponse(resp, body, truncated)}
}

func (r *Request) responseBytes() ([]byte, error) {
	resp, err := r.response()
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// response sends the request, reads the body with the configured limit, and
// applies the ErrorCheck and status handling. The returned *Response is
// non-nil whenever a complete response was received, even with an error.
func (r *Request) response() (*Response, error) {
	if r == nil {
		return nil, errors.New("req: nil request")
	}
	resp, err := r.Do()
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, errors.New("req: HTTP client returned a nil response")
	}
	defer func() {
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()

	if !isSuccess(resp.StatusCode) {
		return r.errorFromResponse(resp)
	}

	limit := r.responseLimit()
	body, truncated, err := readLimited(resp.Body, limit)
	if err != nil {
		return nil, fmt.Errorf("req: read response body: %w", err)
	}
	if truncated {
		return nil, fmt.Errorf("%w: limit is %d bytes", ErrResponseTooLarge, limit)
	}
	info := newResponse(resp, body, false)
	if err := r.checkResponse(info); err != nil {
		return info, err
	}
	return info, nil
}

// errorFromResponse reads the bounded body of a non-2xx response and returns
// it with a *ResponseError. A read failure is joined so the HTTP status stays
// visible to errors.As; the ErrorCheck is skipped because the body is
// incomplete, and no *Response is returned.
func (r *Request) errorFromResponse(resp *http.Response) (*Response, error) {
	body, truncated, readErr := readLimited(resp.Body, maxErrorBodyBytes)
	info := newResponse(resp, body, truncated)
	if readErr != nil {
		return nil, errors.Join(&ResponseError{Response: *info},
			fmt.Errorf("req: read error response body: %w", readErr))
	}
	if err := r.checkResponse(info); err != nil {
		return info, err
	}
	return info, &ResponseError{Response: *info}
}

// checkResponse runs the effective ErrorCheck and wraps a reported error.
func (r *Request) checkResponse(info *Response) error {
	check := r.errorCheck
	if !r.errorCheckSet && r.client != nil {
		check = r.client.errorCheck
	}
	if check == nil {
		return nil
	}
	if err := check(info); err != nil {
		return &ResponseError{Response: *info, Err: err}
	}
	return nil
}

func newResponse(resp *http.Response, body []byte, truncated bool) *Response {
	return &Response{
		StatusCode:    resp.StatusCode,
		Status:        resp.Status,
		Header:        resp.Header.Clone(),
		Body:          body,
		BodyTruncated: truncated,
	}
}

func isSuccess(code int) bool {
	return code >= 200 && code <= 299
}

func (r *Request) responseLimit() int64 {
	limit := DefaultMaxResponseBytes
	if r.client != nil && r.client.maxResponseBytes > 0 {
		limit = r.client.maxResponseBytes
	}
	if r.maxResponseBytes > 0 {
		limit = r.maxResponseBytes
	}
	return limit
}

func decodeJSONResponse(data []byte, v any, useNumber, strict bool) error {
	if !useNumber && !strict {
		if err := json.Unmarshal(data, v); err != nil {
			return fmt.Errorf("req: decode JSON response: %w", err)
		}
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if useNumber {
		decoder.UseNumber()
	}
	if strict {
		decoder.DisallowUnknownFields()
	}
	// Check the complete document before decoding so trailing data cannot
	// silently succeed or partially modify v (matching Unmarshal semantics).
	if !json.Valid(data) {
		var discard any
		err := json.Unmarshal(data, &discard)
		return fmt.Errorf("req: decode JSON response: %w", err)
	}
	if err := decoder.Decode(v); err != nil {
		return fmt.Errorf("req: decode JSON response: %w", err)
	}
	return nil
}

func readLimited(reader io.Reader, limit int64) ([]byte, bool, error) {
	if reader == nil {
		return nil, false, nil
	}
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if int64(len(data)) > limit {
		return data[:limit], true, err
	}
	return data, false, err
}
