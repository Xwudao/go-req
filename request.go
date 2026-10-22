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

	header   http.Header
	queryOps []queryOperation

	body        io.Reader
	bodyFactory func() io.Reader

	maxResponseBytes int64
	jsonUseNumber    bool
	jsonStrict       bool
	errorJSONTarget  any

	debugSet bool
	debug    bool

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

// ErrorJSON sets a target that receives the decoded JSON body of a non-2xx
// response.
//
// It lets an API use one JSON shape for successful responses and another for
// errors. The target is only touched by ToJSON, ToBytes, ToString, and
// ToWriter when the response status is outside 2xx; successful responses leave
// it unchanged. An empty error body also leaves it unchanged.
//
// The raw error body remains available in *StatusError, bounded to 64 KiB. If
// decoding fails, the returned error still exposes *StatusError and also
// matches ErrErrorBodyDecode. Decoding ignores DisallowUnknownFields; UseNumber
// is honored for interface or json.Number targets.
//
// Do never reads or decodes the response body, so ErrorJSON does not apply to
// the raw Do path.
func (r *Request) ErrorJSON(v any) *Request {
	if r.err != nil {
		return r
	}
	if v == nil {
		r.setErr(errors.New("req: ErrorJSON target must not be nil"))
		return r
	}
	r.errorJSONTarget = v
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

	target, err := resolveURL(r.client.baseURL, r.rawURL)
	if err != nil {
		return nil, err
	}

	if len(r.queryOps) > 0 {
		query, err := url.ParseQuery(target.RawQuery)
		if err != nil {
			return nil, fmt.Errorf("req: parse query in URL %q: %w", r.rawURL, err)
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

	httpReq, err := http.NewRequestWithContext(ctx, r.method, target.String(), body)
	if err != nil {
		return nil, fmt.Errorf("req: build HTTP request: %w", err)
	}
	httpReq.Header = header
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
	return r.client.Do(req)
}
