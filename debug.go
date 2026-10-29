package req

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// DefaultDebugMaxBodySize is the default maximum number of request or response
// body bytes captured for debug output. It is 64 KiB.
const DefaultDebugMaxBodySize int64 = 64 << 10

type debugConfig struct {
	enabled       bool
	writer        io.Writer
	body          bool
	maxBodySize   int64
	redactHeaders []string
	redactQuery   []string
	redactJSON    []string
}

func newDebugConfig() debugConfig {
	return debugConfig{
		body:        true,
		maxBodySize: DefaultDebugMaxBodySize,
	}
}

// debugContextKey carries a builder-level Debug override through Build and
// Client.Do.
type debugContextKey struct{}

// WithDebug enables or disables debug output for the Client.
//
// Debug is disabled by default. When enabled, output goes to os.Stdout unless
// WithDebugWriter is used. Enabling debug is an explicit opt-in to logging
// request and response bodies; use WithDebugBody(false) when bodies may contain
// data that must not be logged.
func WithDebug(enabled bool) Option {
	return func(c *Client) error {
		c.debug.enabled = enabled
		return nil
	}
}

// WithDebugWriter sets the destination for debug output.
//
// The writer must not be nil. Debug output is still disabled until WithDebug
// is used (or a Request is built with Debug(true)).
func WithDebugWriter(writer io.Writer) Option {
	return func(c *Client) error {
		if writer == nil {
			return configError("debug writer must not be nil")
		}
		c.debug.writer = writer
		return nil
	}
}

// WithDebugBody enables or disables request and response body logging.
//
// Bodies are logged by default when debug is enabled. Binary bodies are never
// printed: their content type and size are reported instead. Captured bodies
// are additionally limited by WithDebugMaxBodySize.
func WithDebugBody(enabled bool) Option {
	return func(c *Client) error {
		c.debug.body = enabled
		return nil
	}
}

// WithDebugMaxBodySize sets the maximum number of bytes captured from a request
// or response body for debug output.
//
// The default is DefaultDebugMaxBodySize (64 KiB). n must be positive. The
// limit affects debug output only; the HTTP body itself is never truncated.
func WithDebugMaxBodySize(n int64) Option {
	return func(c *Client) error {
		if n <= 0 {
			return configError("debug maximum body size must be positive")
		}
		c.debug.maxBodySize = n
		return nil
	}
}

// DebugError returns the most recent error produced while writing debug
// output, or nil.
//
// Debug write failures never affect the result of an HTTP request.
func (c *Client) DebugError() error {
	if c == nil {
		return nil
	}
	c.debugErrMu.Lock()
	defer c.debugErrMu.Unlock()
	return c.debugErr
}

func (c *Client) writeDebug(output string) {
	if output == "" {
		return
	}

	c.debugWriteMu.Lock()
	writer := c.debug.writer
	if writer == nil {
		writer = os.Stdout
	}
	n, err := io.WriteString(writer, output)
	if err == nil && n != len(output) {
		err = io.ErrShortWrite
	}
	c.debugWriteMu.Unlock()

	if err != nil {
		c.debugErrMu.Lock()
		c.debugErr = fmt.Errorf("req: write debug output: %w", err)
		c.debugErrMu.Unlock()
	}
}

func (c *Client) doDebug(req *http.Request) (*http.Response, error) {
	cfg := c.debug
	start := time.Now()
	id := debugSequence.Add(1)
	write := func(output string) { c.writeDebug(fmt.Sprintf("Request-ID: %d\n", id) + output) }
	hasRequestBody := req.Body != nil && req.Body != http.NoBody

	var requestCapture *debugCapture
	sendReq := req
	logRequest := func(snapshot debugBodySnapshot) {
		write(formatDebugRequest(redactConfiguredRequest(req, cfg), redactConfiguredBody(req.Header.Get("Content-Type"), snapshot, cfg), hasRequestBody, cfg.body))
	}

	if cfg.body && hasRequestBody {
		// Shallow-copy the request so replacing Body for capture does not mutate
		// the caller's request. All other request semantics stay unchanged.
		sendReq = new(http.Request)
		*sendReq = *req
		requestCapture = newDebugCapture(cfg.maxBodySize, logRequest)
		sendReq.Body = &debugReadCloser{
			ReadCloser: req.Body,
			capture:    requestCapture,
		}
	} else {
		logRequest(debugBodySnapshot{})
	}

	resp, err := c.clientFor(sendReq).Do(sendReq)
	headerDuration := time.Since(start)
	if requestCapture != nil {
		// Custom RoundTrippers are not required to read or close the body.
		requestCapture.finish()
	}
	if err != nil {
		write(formatDebugResponseError(redactConfiguredError(err, cfg), time.Since(start)))
		return nil, err
	}
	if resp == nil {
		err := errors.New("req: HTTP client returned a nil response")
		write(formatDebugResponseError(err, time.Since(start)))
		return nil, err
	}
	if !cfg.body {
		write(formatDebugResponse(redactConfiguredResponse(resp, cfg), debugBodySnapshot{}, false, time.Since(start)) + fmt.Sprintf("Headers-Duration: %s\n", headerDuration))
		return resp, nil
	}
	if resp.Body == nil || resp.Body == http.NoBody {
		write(formatDebugResponse(redactConfiguredResponse(resp, cfg), debugBodySnapshot{}, true, time.Since(start)) + fmt.Sprintf("Headers-Duration: %s\n", headerDuration))
		return resp, nil
	}

	resp.Body = &debugReadCloser{
		ReadCloser: resp.Body,
		capture: newDebugCapture(cfg.maxBodySize, func(snapshot debugBodySnapshot) {
			write(formatDebugResponse(redactConfiguredResponse(resp, cfg), redactConfiguredBody(resp.Header.Get("Content-Type"), snapshot, cfg), true, time.Since(start)) + fmt.Sprintf("Headers-Duration: %s\nBody-Duration: %s\n", headerDuration, time.Since(start)-headerDuration))
		}),
	}
	return resp, nil
}

var debugSequence atomic.Uint64

type debugBodySnapshot struct {
	Data      []byte
	Total     int64
	Truncated bool
	Complete  bool
}

type debugCapture struct {
	mu        sync.Mutex
	limit     int64
	data      []byte
	total     int64
	truncated bool
	complete  bool
	once      sync.Once
	onDone    func(debugBodySnapshot)
}

func newDebugCapture(limit int64, onDone func(debugBodySnapshot)) *debugCapture {
	return &debugCapture{limit: limit, onDone: onDone}
}

func (c *debugCapture) observe(p []byte, eof bool) {
	c.mu.Lock()
	c.total += int64(len(p))
	if remaining := c.limit - int64(len(c.data)); remaining > 0 {
		n := int64(len(p))
		if n > remaining {
			n = remaining
		}
		c.data = append(c.data, p[:int(n)]...)
	}
	if c.total > c.limit {
		c.truncated = true
	}
	if eof {
		c.complete = true
	}
	c.mu.Unlock()

	if eof {
		c.finish()
	}
}

func (c *debugCapture) finish() {
	c.once.Do(func() {
		c.mu.Lock()
		snapshot := debugBodySnapshot{
			Data:      append([]byte(nil), c.data...),
			Total:     c.total,
			Truncated: c.truncated,
			Complete:  c.complete,
		}
		c.mu.Unlock()

		if c.onDone != nil {
			c.onDone(snapshot)
		}
	})
}

type debugReadCloser struct {
	io.ReadCloser
	capture *debugCapture
}

func (d *debugReadCloser) Read(p []byte) (int, error) {
	n, err := d.ReadCloser.Read(p)
	if n > 0 {
		d.capture.observe(p[:n], err == io.EOF)
	} else if err == io.EOF {
		d.capture.observe(nil, true)
	}
	return n, err
}

func (d *debugReadCloser) Close() error {
	err := d.ReadCloser.Close()
	d.capture.finish()
	return err
}

func formatDebugRequest(req *http.Request, snapshot debugBodySnapshot, hasBody, bodyEnabled bool) string {
	var b strings.Builder
	b.WriteString("\n===== REQUEST =====\n")
	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	fmt.Fprintf(&b, "%s %s\n", method, redactDebugURL(req.URL))
	b.WriteString("Headers:\n")
	writeDebugHeaders(&b, req.Header)
	if bodyEnabled {
		writeDebugBody(&b, req.Header.Get("Content-Type"), snapshot, hasBody)
	} else {
		b.WriteString("Body: [disabled]\n")
	}
	return b.String()
}

func formatDebugResponse(resp *http.Response, snapshot debugBodySnapshot, bodyEnabled bool, elapsed time.Duration) string {
	var b strings.Builder
	b.WriteString("\n===== RESPONSE =====\n")
	status := resp.Status
	if status == "" {
		status = fmt.Sprintf("%d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	fmt.Fprintf(&b, "Status: %s\n", status)
	b.WriteString("Headers:\n")
	writeDebugHeaders(&b, resp.Header)
	fmt.Fprintf(&b, "Duration: %s\n", elapsed.Round(time.Microsecond))
	if bodyEnabled {
		hasBody := resp.Body != nil && resp.Body != http.NoBody
		writeDebugBody(&b, resp.Header.Get("Content-Type"), snapshot, hasBody)
	} else {
		b.WriteString("Body: [disabled]\n")
	}
	return b.String()
}

func formatDebugResponseError(err error, elapsed time.Duration) string {
	var b strings.Builder
	b.WriteString("\n===== RESPONSE =====\n")
	fmt.Fprintf(&b, "Error: %s\n", redactDebugError(err))
	fmt.Fprintf(&b, "Duration: %s\n", elapsed.Round(time.Microsecond))
	return b.String()
}

func writeDebugHeaders(b *strings.Builder, headers http.Header) {
	if len(headers) == 0 {
		b.WriteString("  (none)\n")
		return
	}
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if isSensitiveDebugName(key) {
			fmt.Fprintf(b, "  %s: [REDACTED]\n", key)
			continue
		}
		values := headers[key]
		if len(values) == 0 {
			fmt.Fprintf(b, "  %s:\n", key)
			continue
		}
		for _, value := range values {
			fmt.Fprintf(b, "  %s: %s\n", key, sanitizeDebugText(value))
		}
	}
}

func writeDebugBody(b *strings.Builder, contentType string, snapshot debugBodySnapshot, hasBody bool) {
	if !hasBody {
		b.WriteString("Body: [empty]\n")
		return
	}
	if snapshot.Total == 0 && !snapshot.Complete {
		b.WriteString("Body: [not read]\n")
		return
	}
	if snapshot.Total == 0 {
		b.WriteString("Body: [empty]\n")
		return
	}

	contentType = strings.TrimSpace(contentType)
	data := snapshot.Data
	if snapshot.Truncated || !snapshot.Complete {
		// The capture limit or early Close can split a UTF-8 rune.
		data = trimIncompleteUTF8(data)
	}
	readable := isReadableDebugBody(contentType, data)
	if contentType == "" {
		contentType = "unknown"
	}
	fmt.Fprintf(b, "Body (%s, %d bytes):\n", contentType, snapshot.Total)

	if !readable {
		b.WriteString("[binary content omitted]\n")
	} else {
		b.Write(data)
		if len(data) > 0 && data[len(data)-1] != '\n' {
			b.WriteByte('\n')
		}
	}
	if snapshot.Truncated {
		fmt.Fprintf(b, "[truncated: captured %d of %d bytes read]\n", len(snapshot.Data), snapshot.Total)
	}
	if !snapshot.Complete {
		fmt.Fprintf(b, "[body not fully read: %d bytes read]\n", snapshot.Total)
	}
}

func isReadableDebugBody(contentType string, data []byte) bool {
	mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	switch {
	case mediaType == "application/octet-stream",
		strings.HasPrefix(mediaType, "image/"),
		strings.HasPrefix(mediaType, "audio/"),
		strings.HasPrefix(mediaType, "video/"),
		strings.HasPrefix(mediaType, "font/"):
		return false
	case mediaType == "":
		if !isProbablyText(data) {
			return false
		}
		detected := strings.ToLower(strings.TrimSpace(strings.SplitN(http.DetectContentType(data), ";", 2)[0]))
		return strings.HasPrefix(detected, "text/")
	case strings.HasPrefix(mediaType, "text/"),
		strings.Contains(mediaType, "json"),
		strings.Contains(mediaType, "xml"),
		strings.Contains(mediaType, "javascript"),
		strings.Contains(mediaType, "ecmascript"),
		strings.Contains(mediaType, "yaml"),
		strings.Contains(mediaType, "graphql"),
		strings.Contains(mediaType, "ndjson"),
		strings.Contains(mediaType, "x-www-form-urlencoded"),
		strings.Contains(mediaType, "csv"),
		strings.Contains(mediaType, "sql"):
		return isProbablyText(data)
	default:
		return false
	}
}

func trimIncompleteUTF8(data []byte) []byte {
	for i := 0; i < utf8.UTFMax-1 && len(data) > 0; i++ {
		r, size := utf8.DecodeLastRune(data)
		if r != utf8.RuneError || size != 1 {
			break
		}
		data = data[:len(data)-1]
	}
	return data
}

func isProbablyText(data []byte) bool {
	if len(data) == 0 {
		return true
	}
	if !utf8.Valid(data) {
		return false
	}
	for _, r := range string(data) {
		if r == 0 {
			return false
		}
		if r < 0x20 && r != '\n' && r != '\r' && r != '\t' {
			return false
		}
	}
	return true
}

func sanitizeDebugText(value string) string {
	return strings.NewReplacer("\r", "\\r", "\n", "\\n").Replace(value)
}

func redactDebugURL(u *url.URL) string {
	if u == nil {
		return "<nil>"
	}
	redacted := *u
	if redacted.User != nil {
		redacted.User = url.User("REDACTED")
	}
	redacted.RawQuery = redactDebugQuery(redacted.RawQuery)
	if redacted.Fragment != "" || redacted.RawFragment != "" {
		// Fragments frequently carry OAuth tokens and are never sent to the
		// server in an HTTP request, so hide them entirely.
		redacted.Fragment = "REDACTED"
		redacted.RawFragment = ""
	}
	return redacted.String()
}

func redactDebugQuery(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return redactRawDebugQuery(rawQuery)
	}
	for key, vals := range values {
		if !isSensitiveDebugName(key) {
			continue
		}
		for i := range vals {
			vals[i] = "REDACTED"
		}
	}
	return values.Encode()
}

func redactRawDebugQuery(rawQuery string) string {
	parts := strings.Split(rawQuery, "&")
	for i, part := range parts {
		key, _, _ := strings.Cut(part, "=")
		decodedKey, err := url.QueryUnescape(key)
		if err != nil {
			decodedKey = key
		}
		if isSensitiveDebugName(decodedKey) {
			parts[i] = key + "=REDACTED"
		}
	}
	return strings.Join(parts, "&")
}

func isSensitiveDebugName(name string) bool {
	normalized := strings.ToLower(strings.NewReplacer("-", "", "_", "", ".", "", " ", "").Replace(name))
	if normalized == "" {
		return false
	}
	switch normalized {
	case "key", "sig", "auth", "code", "jwt", "session":
		return true
	}
	for _, marker := range []string{
		"authorization",
		"cookie",
		"token",
		"secret",
		"password",
		"passwd",
		"apikey",
		"credential",
		"signature",
		"sessionid",
		"accesskey",
		"privatekey",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func redactDebugError(err error) string {
	if err == nil {
		return "<nil>"
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		redactedURL := "[REDACTED]"
		if parsed, parseErr := url.Parse(urlErr.URL); parseErr == nil {
			redactedURL = redactDebugURL(parsed)
		}
		return fmt.Sprintf("%s %s: %s", urlErr.Op, redactedURL, redactDebugError(urlErr.Err))
	}
	return err.Error()
}

// debugContextValue returns the builder-level override, if any.
func debugContextValue(ctx context.Context) (bool, bool) {
	if ctx == nil {
		return false, false
	}
	value, ok := ctx.Value(debugContextKey{}).(bool)
	return value, ok
}
