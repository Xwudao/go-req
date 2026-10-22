package req

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// StatusError is returned by ToJSON, ToBytes, and ToString when the server
// responds with a status outside the 2xx range.
//
// Body contains at most 64 KiB of the response body. It is not an error for the
// body to be truncated; the cap protects against unbounded error payloads.
type StatusError struct {
	StatusCode int
	Status     string
	Header     http.Header
	Body       []byte
	// BodyTruncated reports whether Body exceeded the error capture limit.
	BodyTruncated bool
}

// Error implements error.
func (e *StatusError) Error() string {
	if e.Status != "" {
		return fmt.Sprintf("req: unexpected HTTP status: %s", e.Status)
	}
	return fmt.Sprintf("req: unexpected HTTP status: %d", e.StatusCode)
}

// DecodeJSON decodes the captured error body into v.
//
// An empty body succeeds and leaves v unchanged. Decoding does not use the
// UseNumber or DisallowUnknownFields request options. If Body was truncated,
// decoding will usually fail; check BodyTruncated to distinguish that case.
func (e *StatusError) DecodeJSON(v any) error {
	if e == nil {
		return errors.New("req: nil status error")
	}
	if v == nil {
		return errors.New("req: DecodeJSON target must not be nil")
	}
	if len(e.Body) == 0 {
		return nil
	}
	return decodeErrorJSON(e.Body, v, false)
}

// ToJSON sends the request, requires a 2xx status, decodes the response body as
// JSON into v, and closes the body.
//
// An empty body, including a 204 No Content response, is treated as success and
// leaves v unchanged. A JSON null is decoded normally by encoding/json.
//
// Non-2xx responses return *StatusError. Network, read, and decode errors are
// returned with their original error chain intact.
func (r *Request) ToJSON(v any) error {
	if v == nil {
		return errors.New("req: ToJSON target must not be nil")
	}
	data, err := r.responseBytes()
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	return decodeJSONResponse(data, v, r.jsonUseNumber, r.jsonStrict)
}

// ToBytes sends the request, requires a 2xx status, reads the response body,
// and closes it.
//
// The body is limited by DefaultMaxResponseBytes or the value supplied to
// WithMaxResponseBytes. ErrResponseTooLarge is returned when the limit is
// exceeded. For large downloads, use Do and stream the body with io.Copy.
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

func (r *Request) responseBytes() ([]byte, error) {
	body, statusErr, err := r.readResponse()
	if statusErr != nil {
		return nil, r.statusError(statusErr, err)
	}
	return body, err
}

// readResponse sends the request and reads the body with the configured limit.
// A non-2xx response returns a StatusError and the error encountered while
// reading its capture body, if any. A successful response returns its body.
func (r *Request) readResponse() ([]byte, *StatusError, error) {
	resp, err := r.Do()
	if err != nil {
		return nil, nil, err
	}
	if resp == nil {
		return nil, nil, errors.New("req: HTTP client returned a nil response")
	}
	defer func() {
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		statusErr, readErr := readStatusError(resp)
		return nil, statusErr, readErr
	}

	limit := r.responseLimit()
	body, truncated, err := readLimited(resp.Body, limit)
	if err != nil {
		return nil, nil, fmt.Errorf("req: read response body: %w", err)
	}
	if truncated {
		return nil, nil, fmt.Errorf("%w: limit is %d bytes", ErrResponseTooLarge, limit)
	}
	return body, nil, nil
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

// statusError combines a StatusError, an ErrorJSON decode failure, and an
// error-body read failure without hiding the HTTP status from errors.As.
func (r *Request) statusError(statusErr *StatusError, readErr error) error {
	errs := []error{statusErr}
	if readErr != nil {
		errs = append(errs, readErr)
	}
	if r.errorJSONTarget != nil && len(statusErr.Body) > 0 {
		if err := decodeErrorJSON(statusErr.Body, r.errorJSONTarget, r.jsonUseNumber); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) == 1 {
		return statusErr
	}
	return errors.Join(errs...)
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

func decodeErrorJSON(data []byte, v any, useNumber bool) error {
	if !useNumber {
		if err := json.Unmarshal(data, v); err != nil {
			return fmt.Errorf("%w: %w", ErrErrorBodyDecode, err)
		}
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	// Match Unmarshal semantics: reject trailing data after the document.
	if !json.Valid(data) {
		var discard any
		err := json.Unmarshal(data, &discard)
		return fmt.Errorf("%w: %w", ErrErrorBodyDecode, err)
	}
	if err := decoder.Decode(v); err != nil {
		return fmt.Errorf("%w: %w", ErrErrorBodyDecode, err)
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
