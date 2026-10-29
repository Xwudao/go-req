package req

import (
	"errors"
	"fmt"
	"io"
)

// ToWriter sends the request and streams a successful response to dst.
// It closes the response body, never dst, and returns the bytes written even
// on failure. Non-2xx responses return *ResponseError without writing to dst;
// the ErrorCheck runs only for them, because a successful body is streamed
// rather than buffered.
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
	if !isSuccess(resp.StatusCode) {
		_, err := r.errorFromResponse(resp)
		return 0, err
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
