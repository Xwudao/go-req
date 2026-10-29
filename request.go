package req

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"time"
)

// Request is a mutable request builder created by a Client.
//
// Builder methods record the first construction error and never panic on
// invalid input. The error is returned by Build, Do, ToJSON, ToBytes,
// ToString, or ToWriter. A Request must not be modified by multiple goroutines
// concurrently.
//
// JSON and Form bodies are encoded when their method is called, so they can be
// built more than once. A reader passed to Body is not buffered: it is consumed
// once and may already be at EOF on a second Build. req never closes a reader
// supplied to Body.
type Request struct {
	client *Client
	ctx    context.Context
	method string
	rawURL string

	header     http.Header
	queryOps   []queryOperation
	pathParams map[string]string

	body        io.Reader
	bodyFactory func() io.Reader

	maxResponseBytes int64
	jsonUseNumber    bool
	jsonStrict       bool

	errorCheck    ErrorCheck
	errorCheckSet bool

	beforeRequest []func(*http.Request) error
	timeout       time.Duration

	debugSet bool
	debug    bool

	noRedirect bool

	err error
}

type queryOperation struct {
	key    string
	values []string
	add    bool
}

func (r *Request) setErr(err error) {
	if r.err == nil {
		r.err = err
	}
}

// Header sets a request header, replacing any values already set for key on
// this builder. Default headers from the Client are overridden in Build.
//
// As in net/http, Host, Content-Length, and Transfer-Encoding are not ordinary
// headers. Use Build and set http.Request.Host or ContentLength directly when
// those fields need to be overridden.
func (r *Request) Header(key, value string) *Request {
	if r.err != nil {
		return r
	}
	r.header.Set(key, value)
	return r
}

// Headers copies all values from h, replacing matching keys already set on
// this builder. Unrelated builder headers are kept.
func (r *Request) Headers(h http.Header) *Request {
	if r.err != nil {
		return r
	}
	for key, values := range h {
		r.header[http.CanonicalHeaderKey(key)] = append([]string(nil), values...)
	}
	return r
}

// Query sets query parameter key to value, replacing all existing values for
// that key, including values already present in the request URL.
func (r *Request) Query(key, value string) *Request {
	if r.err != nil {
		return r
	}
	r.queryOps = append(r.queryOps, queryOperation{
		key:    key,
		values: []string{value},
	})
	return r
}

// AddQuery appends value to query parameter key. Existing values, including
// those in the request URL and earlier AddQuery calls, are retained.
func (r *Request) AddQuery(key, value string) *Request {
	if r.err != nil {
		return r
	}
	r.queryOps = append(r.queryOps, queryOperation{
		key:    key,
		values: []string{value},
		add:    true,
	})
	return r
}

// QueryValues replaces the values of each key in values. Other query
// parameters are left untouched. A key with a nil or empty value slice is
// removed.
func (r *Request) QueryValues(values url.Values) *Request {
	if r.err != nil {
		return r
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		vals := append([]string(nil), values[key]...)
		r.queryOps = append(r.queryOps, queryOperation{key: key, values: vals})
	}
	return r
}

// PathParam sets the value of a {name} placeholder in the request URL path.
//
// The value is escaped with url.PathEscape, so a slash in value never adds a
// path segment. It must not be empty, ".", or "..". When any path parameter is
// set, every placeholder in the URL path must be filled and every parameter
// must match a placeholder; otherwise Build returns an error.
//
//	client.Get(ctx, "/users/{id}/posts/{pid}").PathParam("id", "42").PathParam("pid", "7")
func (r *Request) PathParam(name, value string) *Request {
	if r.err != nil {
		return r
	}
	if err := validatePathParam(name, value); err != nil {
		r.setErr(err)
		return r
	}
	if r.pathParams == nil {
		r.pathParams = make(map[string]string)
	}
	r.pathParams[name] = value
	return r
}

// PathParams sets several path parameters at once. It follows the rules of
// PathParam and replaces values already set for the same names.
func (r *Request) PathParams(params map[string]string) *Request {
	for name, value := range params {
		r.PathParam(name, value)
	}
	return r
}

// BeforeRequest adds a hook that runs on the built *http.Request after the
// Client's hooks. See WithBeforeRequest.
func (r *Request) BeforeRequest(hook func(*http.Request) error) *Request {
	if r.err != nil {
		return r
	}
	if hook == nil {
		r.setErr(errors.New("req: nil before-request hook"))
		return r
	}
	r.beforeRequest = append(r.beforeRequest, hook)
	return r
}

// NoRedirect makes this request stop at the first redirect response instead
// of following it. The 3xx response is returned as is; use ToLocation to read
// its target, or Do to inspect it directly.
//
// Like Debug, the choice is carried by the request context, so it also applies
// if Build is followed by Client.Do. It works with WithHTTPClient as well,
// because a shallow copy of the http.Client is used for this request only.
func (r *Request) NoRedirect() *Request {
	if r.err != nil {
		return r
	}
	r.noRedirect = true
	return r
}

// Timeout limits the whole request, including reading the response body, to d.
//
// It is applied when the request is sent through Do or a response helper, by
// deriving a context from the request context. Build does not apply it. The
// Client's WithTimeout still applies; the shorter limit wins. d must be
// positive.
func (r *Request) Timeout(d time.Duration) *Request {
	if r.err != nil {
		return r
	}
	if d <= 0 {
		r.setErr(errors.New("req: request timeout must be positive"))
		return r
	}
	r.timeout = d
	return r
}

// JSON encodes v as JSON and sets the request body.
//
// Content-Type is set to application/json unless it was already set explicitly
// on this builder, either before or after JSON is called. Encoding happens
// immediately; an encoding error is returned later by Build, Do, or a response
// helper. The encoded body can be built repeatedly.
func (r *Request) JSON(v any) *Request {
	if r.err != nil {
		return r
	}
	data, err := json.Marshal(v)
	if err != nil {
		r.setErr(fmt.Errorf("req: encode JSON body: %w", err))
		return r
	}
	r.body = nil
	r.bodyFactory = func() io.Reader {
		return bytes.NewReader(data)
	}
	r.setDefaultContentType("application/json")
	return r
}

// Form encodes values as application/x-www-form-urlencoded and sets the
// request body.
//
// Content-Type is set unless it was already set explicitly. The body can be
// built repeatedly.
func (r *Request) Form(values url.Values) *Request {
	if r.err != nil {
		return r
	}
	data := values.Encode()
	r.body = nil
	r.bodyFactory = func() io.Reader {
		return bytes.NewReader([]byte(data))
	}
	r.setDefaultContentType("application/x-www-form-urlencoded")
	return r
}

// Body sets the request body to body.
//
// The reader is not buffered and is not closed by req. It is consumed once when
// the request is sent; calling Build or Do again reuses the same reader at its
// current position.
func (r *Request) Body(body io.Reader) *Request {
	if r.err != nil {
		return r
	}
	r.body = body
	r.bodyFactory = nil
	return r
}

// MaxResponseBytes overrides the buffered response limit for this request.
// It does not limit Do or ToWriter. n must be positive and less than MaxInt64.
func (r *Request) MaxResponseBytes(n int64) *Request {
	if n <= 0 || n == math.MaxInt64 {
		r.setErr(configError("maximum response size must be positive and less than MaxInt64"))
	} else {
		r.maxResponseBytes = n
	}
	return r
}

// UseNumber preserves JSON numbers when decoding into interface values.
func (r *Request) UseNumber(enabled bool) *Request {
	r.jsonUseNumber = enabled
	return r
}

// DisallowUnknownFields rejects JSON object fields absent from the target struct.
func (r *Request) DisallowUnknownFields(enabled bool) *Request {
	r.jsonStrict = enabled
	return r
}

// ErrorCheck overrides the Client's ErrorCheck for this request. A nil check
// disables error checking, so only the HTTP status decides success.
//
// It applies to ToJSON, ToBytes, ToString, and the non-2xx path of ToWriter.
// Do never reads the response body, so no check is applied to it.
func (r *Request) ErrorCheck(check ErrorCheck) *Request {
	if r.err != nil {
		return r
	}
	r.errorCheck = check
	r.errorCheckSet = true
	return r
}

// Cookie adds cookie to the Cookie request header.
func (r *Request) Cookie(cookie *http.Cookie) *Request {
	if r.err != nil {
		return r
	}
	if cookie == nil {
		r.setErr(errors.New("req: nil cookie"))
		return r
	}
	r.header.Add("Cookie", cookie.String())
	return r
}

// BasicAuth sets the Authorization header using HTTP Basic authentication.
func (r *Request) BasicAuth(username, password string) *Request {
	if r.err != nil {
		return r
	}
	// Let net/http provide the exact standard encoding.
	req := &http.Request{Header: r.header}
	req.SetBasicAuth(username, password)
	return r
}

// BearerToken sets the Authorization header to a Bearer token.
func (r *Request) BearerToken(token string) *Request {
	if r.err != nil {
		return r
	}
	r.header.Set("Authorization", "Bearer "+token)
	return r
}

// Debug enables or disables debug logging for this request only.
//
// It overrides the Client's WithDebug setting. Writer, body, and size limits
// still come from the Client. When set, the choice is carried by the request
// context, so it also applies if Build is followed by Client.Do.
func (r *Request) Debug(enabled bool) *Request {
	if r.err != nil {
		return r
	}
	r.debugSet = true
	r.debug = enabled
	return r
}

func (r *Request) setDefaultContentType(value string) {
	if _, exists := r.header["Content-Type"]; !exists {
		r.header.Set("Content-Type", value)
	}
}

// Build creates the final *http.Request without sending it.
//
// It resolves the URL, applies query parameters and headers, and creates the
// body. For a reader supplied to Body, Build does not rewind or clone the
// reader. JSON and Form bodies are rebuilt from their encoded bytes.
//
// The returned request is independent of the builder's header and query state,
// but shares any reader supplied to Body.
func (r *Request) Build() (*http.Request, error) {
	if r == nil {
		return nil, errors.New("req: nil request")
	}
	if r.err != nil {
		return nil, r.err
	}
	if r.client == nil {
		return nil, errors.New("req: request has no client")
	}
	if r.client.err != nil {
		return nil, r.client.err
	}
	if r.ctx == nil {
		return nil, errors.New("req: nil context")
	}

	rawURL := r.rawURL
	if len(r.pathParams) > 0 {
		expanded, err := expandPathParams(rawURL, r.pathParams)
		if err != nil {
			return nil, err
		}
		rawURL = expanded
	}
	target, err := resolveURL(r.client.baseURL, rawURL)
	if err != nil {
		return nil, err
	}

	if len(r.queryOps) > 0 || len(r.client.query) > 0 {
		query, err := url.ParseQuery(target.RawQuery)
		if err != nil {
			return nil, fmt.Errorf("req: parse query in URL %q: %w", r.rawURL, err)
		}
		for key, values := range r.client.query {
			if _, exists := query[key]; !exists {
				query[key] = append([]string(nil), values...)
			}
		}
		for _, op := range r.queryOps {
			switch {
			case op.add:
				query[op.key] = append(query[op.key], op.values...)
			case op.values == nil:
				query.Del(op.key)
			default:
				query[op.key] = append([]string(nil), op.values...)
			}
		}
		target.RawQuery = query.Encode()
		target.ForceQuery = false
	}

	header := r.client.header.Clone()
	if header == nil {
		header = make(http.Header)
	}
	for key, values := range r.header {
		header[key] = append([]string(nil), values...)
	}

	var body io.Reader
	switch {
	case r.bodyFactory != nil:
		body = r.bodyFactory()
	case r.body != nil:
		body = r.body
		// net/http closes a request body that implements io.ReadCloser. The
		// caller owns readers passed to Body, so hide Close behind a no-op.
		if _, ok := body.(io.ReadCloser); ok {
			body = io.NopCloser(body)
		}
	}

	ctx := r.ctx
	if r.debugSet {
		ctx = context.WithValue(ctx, debugContextKey{}, r.debug)
	}
	if r.noRedirect {
		ctx = context.WithValue(ctx, noRedirectContextKey{}, true)
	}

	httpReq, err := http.NewRequestWithContext(ctx, r.method, target.String(), body)
	if err != nil {
		return nil, fmt.Errorf("req: build HTTP request: %w", err)
	}
	httpReq.Header = header
	for _, hook := range r.client.beforeRequest {
		if err := hook(httpReq); err != nil {
			return nil, fmt.Errorf("req: before-request hook: %w", err)
		}
	}
	for _, hook := range r.beforeRequest {
		if err := hook(httpReq); err != nil {
			return nil, fmt.Errorf("req: before-request hook: %w", err)
		}
	}
	return httpReq, nil
}

// Do builds and sends the request.
//
// The returned body belongs to the caller and is not closed or read by Do.
// HTTP error statuses are not treated as transport errors.
func (r *Request) Do() (*http.Response, error) {
	if r == nil {
		return nil, errors.New("req: nil request")
	}
	req, err := r.Build()
	if err != nil {
		return nil, err
	}
	if r.timeout <= 0 {
		return r.client.Do(req)
	}
	ctx, cancel := context.WithTimeout(req.Context(), r.timeout)
	resp, err := r.client.Do(req.WithContext(ctx))
	if err != nil || resp == nil || resp.Body == nil {
		cancel()
		return resp, err
	}
	resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

// cancelOnClose releases a per-request timeout when the body is closed.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelOnClose) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}
