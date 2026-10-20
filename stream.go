package req

import (
	"errors"
	"fmt"
	"io"
	"net/http"
)

// ToWriter sends the request and streams a successful response to dst.
// It closes the response body, never dst, and returns the bytes written even
// on failure. Non-2xx responses return *StatusError without writing to dst.
// Unlike buffered helpers, it has no response size limit. Use context deadlines
// and/or a size-limiting writer when downloading untrusted content.
func (r *Request) ToWriter(dst io.Writer) (int64, error) {
	if dst == nil {
		return 0, errors.New("req: nil response writer")
	}
	resp, err := r.Do()
	if err != nil {
		return 0, err
	}
	if resp == nil {
		return 0, errors.New("req: HTTP client returned a nil response")
	}
	if resp.Body != nil {
		defer resp.Body.Close()
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return 0, responseStatusError(resp)
	}
	if resp.Body == nil {
		return 0, nil
	}
	n, err := io.Copy(dst, resp.Body)
	if err != nil {
		return n, fmt.Errorf("req: stream response body: %w", err)
	}
	return n, nil
}

func responseStatusError(resp *http.Response) error {
	body, truncated, err := readLimited(resp.Body, maxErrorBodyBytes)
	statusErr := &StatusError{StatusCode: resp.StatusCode, Status: resp.Status,
		Header: resp.Header.Clone(), Body: body, BodyTruncated: truncated}
	if err != nil {
		return errors.Join(statusErr, fmt.Errorf("req: read error response body: %w", err))
	}
	return statusErr
}
