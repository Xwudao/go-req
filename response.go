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
	if !r.jsonUseNumber && !r.jsonStrict {
		if err := json.Unmarshal(data, v); err != nil {
			return fmt.Errorf("req: decode JSON response: %w", err)
		}
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if r.jsonUseNumber {
		decoder.UseNumber()
	}
	if r.jsonStrict {
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

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, responseStatusError(resp)
	}

	limit := DefaultMaxResponseBytes
	if r.client != nil && r.client.maxResponseBytes > 0 {
		limit = r.client.maxResponseBytes
	}
	if r.maxResponseBytes > 0 {
		limit = r.maxResponseBytes
	}
	body, truncated, err := readLimited(resp.Body, limit)
	if err != nil {
		return nil, fmt.Errorf("req: read response body: %w", err)
	}
	if truncated {
		return nil, fmt.Errorf("%w: limit is %d bytes", ErrResponseTooLarge, limit)
	}
	return body, nil
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
