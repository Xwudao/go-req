package req

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"math"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// SSEEvent is one event of a text/event-stream response.
type SSEEvent struct {
	// ID is the last event ID seen in the stream, which persists across events
	// as defined by the SSE specification.
	ID string
	// Event is the event type, or "" when the server did not send one.
	Event string
	// Data holds the data lines of the event joined with "\n".
	Data string
	// Retry is the reconnection time sent with this event, or 0.
	Retry time.Duration
}

// JSON decodes the event data as JSON into T.
//
//	chunk, err := ev.JSON[Chunk]()
func (e *SSEEvent) JSON[T any]() (T, error) {
	var value T
	if err := json.Unmarshal([]byte(e.Data), &value); err != nil {
		return value, fmt.Errorf("req: decode SSE event data: %w", err)
	}
	return value, nil
}

// ToSSE sends the request and iterates over the events of a Server-Sent
// Events response. Each range over the result sends a new request.
//
// The response body is closed when the loop ends, whether by break, error, or
// end of stream. A non-2xx response yields a single *ResponseError. The
// ErrorCheck runs on the data of every event, so an API that reports errors
// as events is handled like a buffered response; the yielded error is then a
// *ResponseError whose Body is the event data. A 2xx response with a JSON
// Content-Type is read as a whole and checked once, because some APIs answer
// errors without opening a stream.
//
// After an error is yielded, iteration stops. Each line is limited by the
// response size limit, as is the data of each event.
//
//	for ev, err := range client.Post(ctx, "/chat").JSON(body).ToSSE() {
//	    if err != nil {
//	        return err
//	    }
//	    if ev.Data == "[DONE]" {
//	        break
//	    }
//	    chunk, err := ev.JSON[Chunk]()
//	    ...
//	}
func (r *Request) ToSSE() iter.Seq2[*SSEEvent, error] {
	return func(yield func(*SSEEvent, error) bool) {
		resp, err := r.openStream(true)
		if err != nil {
			yield(nil, err)
			return
		}
		defer resp.Body.Close()

		limit := r.responseLimit()
		scanner := newLimitedScanner(resp.Body, limit)
		scanner.Split(scanSSELines)
		header := resp.Header.Clone()

		var (
			lastID  string
			event   string
			retry   time.Duration
			data    strings.Builder
			hasData bool
			first   = true
		)
		dispatch := func() bool {
			if !hasData {
				event, retry = "", 0
				return true
			}
			ev := &SSEEvent{ID: lastID, Event: event, Data: data.String(), Retry: retry}
			event, retry, hasData = "", 0, false
			data.Reset()
			if err := r.checkStreamItem(resp, header, []byte(ev.Data)); err != nil {
				yield(nil, err)
				return false
			}
			return yield(ev, nil)
		}

		for scanner.Scan() {
			line := scanner.Text()
			if first {
				line = strings.TrimPrefix(line, "\uFEFF")
				first = false
			}
			if line == "" {
				if !dispatch() {
					return
				}
				continue
			}
			if strings.HasPrefix(line, ":") {
				continue
			}
			field, value, found := strings.Cut(line, ":")
			if found {
				value = strings.TrimPrefix(value, " ")
			}
			switch field {
			case "event":
				event = value
			case "data":
				if hasData {
					data.WriteByte('\n')
				}
				data.WriteString(value)
				hasData = true
				if int64(data.Len()) > limit {
					yield(nil, fmt.Errorf("%w: SSE event data exceeds %d bytes", ErrResponseTooLarge, limit))
					return
				}
			case "id":
				if !strings.ContainsRune(value, 0) {
					lastID = value
				}
			case "retry":
				if ms, err := strconv.ParseUint(value, 10, 63); err == nil {
					retry = time.Duration(ms) * time.Millisecond
				}
			}
		}
		if err := scanner.Err(); err != nil {
			yield(nil, streamReadError(err, limit))
			return
		}
		// Be lenient with servers that omit the final blank line.
		dispatch()
	}
}

// ToJSONStream sends the request and iterates over a stream of JSON values,
// one per line (NDJSON / JSON Lines). Blank lines are skipped. Each range
// over the result sends a new request.
//
// Values are decoded with the UseNumber and DisallowUnknownFields settings.
// The ErrorCheck runs on every line before it is decoded. A non-2xx response
// yields a single *ResponseError. After an error is yielded, iteration stops.
// The body is closed when the loop ends. Each line is limited by the response
// size limit.
//
//	for log, err := range client.Get(ctx, "/logs").ToJSONStream[LogEntry]() {
//	    if err != nil {
//	        return err
//	    }
//	    fmt.Println(log.Message)
//	}
func (r *Request) ToJSONStream[T any]() iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var zero T
		resp, err := r.openStream(false)
		if err != nil {
			yield(zero, err)
			return
		}
		defer resp.Body.Close()

		limit := r.responseLimit()
		scanner := newLimitedScanner(resp.Body, limit)
		header := resp.Header.Clone()
		for scanner.Scan() {
			line := bytes.TrimSpace(scanner.Bytes())
			if len(line) == 0 {
				continue
			}
			if err := r.checkStreamItem(resp, header, line); err != nil {
				yield(zero, err)
				return
			}
			var value T
			if err := decodeJSONResponse(line, &value, r.jsonUseNumber, r.jsonStrict); err != nil {
				yield(zero, err)
				return
			}
			if !yield(value, nil) {
				return
			}
		}
		if err := scanner.Err(); err != nil {
			yield(zero, streamReadError(err, limit))
		}
	}
}

// openStream sends the request and returns a 2xx response whose body the
// caller must close. Error responses are read, checked, and closed.
func (r *Request) openStream(sse bool) (*http.Response, error) {
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
	if resp.Body == nil {
		resp.Body = http.NoBody
	}
	if !isSuccess(resp.StatusCode) {
		defer resp.Body.Close()
		_, err := r.errorFromResponse(resp)
		return nil, err
	}
	if sse && isJSONContentType(resp.Header.Get("Content-Type")) {
		defer resp.Body.Close()
		limit := r.responseLimit()
		body, truncated, err := readLimited(resp.Body, limit)
		if err != nil {
			return nil, fmt.Errorf("req: read response body: %w", err)
		}
		if truncated {
			return nil, fmt.Errorf("%w: limit is %d bytes", ErrResponseTooLarge, limit)
		}
		if err := r.checkResponse(newResponse(resp, body, false)); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("req: expected an event stream, got Content-Type %q", resp.Header.Get("Content-Type"))
	}
	return resp, nil
}

// checkStreamItem runs the ErrorCheck on one stream item, presented as a
// response whose body is the item.
func (r *Request) checkStreamItem(resp *http.Response, header http.Header, item []byte) error {
	return r.checkResponse(&Response{
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
		Header:     header,
		Body:       item,
	})
}

func isJSONContentType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return false
	}
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

func newLimitedScanner(reader io.Reader, limit int64) *bufio.Scanner {
	maxToken := limit
	if maxToken > math.MaxInt32 {
		maxToken = math.MaxInt32
	}
	initial := int64(4096)
	if initial > maxToken {
		initial = maxToken
	}
	scanner := bufio.NewScanner(reader)
	// bufio.Scanner needs one spare byte beyond the token it returns.
	scanner.Buffer(make([]byte, 0, initial), int(maxToken)+1)
	return scanner
}

func streamReadError(err error, limit int64) error {
	if errors.Is(err, bufio.ErrTooLong) {
		return fmt.Errorf("%w: a stream line exceeds %d bytes", ErrResponseTooLarge, limit)
	}
	return fmt.Errorf("req: read stream: %w", err)
}

// scanSSELines splits on "\r\n", "\n", or a lone "\r", as SSE requires.
func scanSSELines(data []byte, atEOF bool) (int, []byte, error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		if data[i] == '\n' {
			return i + 1, data[:i], nil
		}
		if i+1 < len(data) {
			if data[i+1] == '\n' {
				return i + 2, data[:i], nil
			}
			return i + 1, data[:i], nil
		}
		if !atEOF {
			return 0, nil, nil
		}
		return i + 1, data[:i], nil
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}
