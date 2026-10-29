// Package req provides a small, standard-library-first HTTP client with a
// chainable request builder.
//
// Client handles configuration, base URL resolution, and default headers.
// Request builds a standard *http.Request lazily and can send it directly.
// Native *http.Request and *http.Response values are used throughout; Do never
// wraps or reads responses. Clients are immutable after construction and safe
// for concurrent use, while each Request builder is intended for one goroutine.
package req

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const (
	// DefaultMaxResponseBytes is the default limit used by ToJSON, ToBytes,
	// and ToString. It is 64 MiB.
	DefaultMaxResponseBytes int64 = 64 << 20

	// maxErrorBodyBytes limits the body copied into a ResponseError. Error
	// responses are never read without a bound. It is 64 KiB.
	maxErrorBodyBytes int64 = 64 << 10
)

// Client is an HTTP client with a small request builder.
//
// A Client is safe for concurrent use after construction. Its configuration is
// immutable; every Get, Post, or NewRequest call creates an independent
// Request builder.
//
// Client.Do passes a caller-provided *http.Request through unchanged. BaseURL
// resolution and default headers apply only to requests created by Client's
// builder methods.
type Client struct {
	httpClient       *http.Client
	baseURL          *url.URL
	header           http.Header
	query            url.Values
	beforeRequest    []func(*http.Request) error
	maxResponseBytes int64
	errorCheck       ErrorCheck
	err              error

	debug        debugConfig
	debugWriteMu sync.Mutex
	debugErrMu   sync.Mutex
	debugErr     error

	// Staged option values. They are consumed by finalize.
	hasHTTPClient  bool
	timeout        time.Duration
	hasTimeout     bool
	transport      http.RoundTripper
	hasTransport   bool
	dialContext    func(ctx context.Context, network, address string) (net.Conn, error)
	hasDialContext bool
	proxyFunc      func(*http.Request) (*url.URL, error)
	proxyOption    string
	hasProxyConfig bool
	checkRedirect  func(req *http.Request, via []*http.Request) error
	redirectOption string
}

func newClient() *Client {
	return &Client{
		header:           make(http.Header),
		maxResponseBytes: DefaultMaxResponseBytes,
		debug:            newDebugConfig(),
	}
}

// New creates a Client with the supplied options.
//
// New never panics on configuration errors. If an option is invalid, the error
// is retained and returned by every request built or executed by the Client;
// Client.Err exposes it directly. Prefer NewWithOptions when the configuration
// is assembled at runtime and errors must be handled immediately.
//
// The default transport is an independent clone of http.DefaultTransport.
// http.DefaultClient and http.DefaultTransport are never modified.
func New(opts ...Option) *Client {
	c := newClient()
	if err := c.applyOptions(opts); err != nil {
		c.err = err
	}
	return c
}

// NewWithOptions is like New but returns configuration errors immediately.
//
// On error it returns a nil Client.
func NewWithOptions(opts ...Option) (*Client, error) {
	c := newClient()
	if err := c.applyOptions(opts); err != nil {
		c.err = err
		return nil, err
	}
	return c, nil
}

func (c *Client) applyOptions(opts []Option) error {
	for _, opt := range opts {
		if opt == nil {
			return configError("nil option")
		}
		if err := opt(c); err != nil {
			return err
		}
	}
	return c.finalize()
}

func (c *Client) finalize() error {
	if c.maxResponseBytes == 0 {
		c.maxResponseBytes = DefaultMaxResponseBytes
	}

	var conflicts []error
	if c.hasHTTPClient {
		if c.hasTimeout {
			conflicts = append(conflicts, configError(
				"WithTimeout cannot be combined with WithHTTPClient; set http.Client.Timeout instead",
			))
		}
		if c.hasTransport {
			conflicts = append(conflicts, configError(
				"WithTransport cannot be combined with WithHTTPClient; set http.Client.Transport instead",
			))
		}
		if c.hasDialContext {
			conflicts = append(conflicts, configError(
				"WithDialContext cannot be combined with WithHTTPClient; configure its transport instead",
			))
		}
	}
	if c.hasTransport && c.hasDialContext {
		conflicts = append(conflicts, configError(
			"WithDialContext cannot be combined with WithTransport; configure http.Transport.DialContext directly",
		))
	}
	if c.hasHTTPClient && c.redirectOption != "" {
		conflicts = append(conflicts, configError(
			"%s cannot be combined with WithHTTPClient; set http.Client.CheckRedirect instead",
			c.redirectOption,
		))
	}
	if c.hasHTTPClient && c.hasProxyConfig {
		conflicts = append(conflicts, configError(
			"%s cannot be combined with WithHTTPClient; set http.Client.Transport.Proxy instead",
			c.proxyOption,
		))
	}
	if c.hasTransport && c.hasProxyConfig {
		if _, ok := c.transport.(*http.Transport); !ok {
			conflicts = append(conflicts, configError(
				"%s cannot be combined with WithTransport using custom RoundTripper %T; only *http.Transport can be cloned to set Proxy",
				c.proxyOption, c.transport,
			))
		}
	}
	if len(conflicts) > 0 {
		return errors.Join(conflicts...)
	}

	if c.hasHTTPClient {
		// WithHTTPClient stores the caller's pointer as-is.
		return nil
	}

	transport, err := c.newTransport()
	if err != nil {
		return err
	}
	c.httpClient = &http.Client{
		Transport:     transport,
		Timeout:       c.timeout,
		CheckRedirect: c.checkRedirect,
	}
	return nil
}

func (c *Client) newTransport() (http.RoundTripper, error) {
	if c.hasTransport {
		if !c.hasProxyConfig {
			return c.transport, nil
		}
		// A caller-supplied *http.Transport is cloned so setting Proxy never
		// mutates the caller's transport. Clone preserves TLS, DialContext,
		// connection pool, and all other settings.
		transport, ok := c.transport.(*http.Transport)
		if !ok {
			return nil, configError(
				"%s cannot be applied to custom RoundTripper %T; only *http.Transport can be cloned to set Proxy",
				c.proxyOption, c.transport,
			)
		}
		clone := transport.Clone()
		clone.Proxy = c.proxyFunc
		return clone, nil
	}

	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, configError(
			"cannot clone http.DefaultTransport of type %T; use WithTransport or WithHTTPClient",
			http.DefaultTransport,
		)
	}

	transport := defaultTransport.Clone()
	if c.hasDialContext {
		transport.DialContext = c.dialContext
	}
	if c.hasProxyConfig {
		transport.Proxy = c.proxyFunc
	}
	return transport, nil
}

// Err returns the Client's configuration error, if any.
func (c *Client) Err() error {
	if c == nil {
		return errors.New("req: nil client")
	}
	return c.err
}

// HTTPClient returns the underlying http.Client.
//
// The returned pointer is the same one that Client.Do uses. Callers must not
// modify it while the Client is in use. For a Client created with
// WithHTTPClient, this is the exact pointer supplied by the caller. If New
// retained a configuration error, HTTPClient may return nil; check Err.
func (c *Client) HTTPClient() *http.Client {
	if c == nil {
		return nil
	}
	return c.httpClient
}

// CloseIdleConnections closes idle connections held by the underlying
// http.Client. Active requests are unaffected.
func (c *Client) CloseIdleConnections() {
	if c != nil && c.httpClient != nil {
		c.httpClient.CloseIdleConnections()
	}
}

// NewRequest starts building a request with the given method and URL.
//
// The URL is resolved, headers are merged, and the request body is created only
// when Build or Do is called. Invalid builder input is recorded and returned by
// those methods.
//
// A Request builder must not be used concurrently by multiple goroutines.
func (c *Client) NewRequest(ctx context.Context, method, rawURL string) *Request {
	r := &Request{
		client: c,
		ctx:    ctx,
		method: method,
		rawURL: rawURL,
		header: make(http.Header),
	}
	if c == nil {
		r.setErr(errors.New("req: nil client"))
		return r
	}
	if c.err != nil {
		r.setErr(c.err)
		return r
	}
	if ctx == nil {
		r.setErr(errors.New("req: nil context"))
	}
	return r
}

// Get starts a GET request.
func (c *Client) Get(ctx context.Context, rawURL string) *Request {
	return c.NewRequest(ctx, http.MethodGet, rawURL)
}

// Post starts a POST request.
func (c *Client) Post(ctx context.Context, rawURL string) *Request {
	return c.NewRequest(ctx, http.MethodPost, rawURL)
}

// Put starts a PUT request.
func (c *Client) Put(ctx context.Context, rawURL string) *Request {
	return c.NewRequest(ctx, http.MethodPut, rawURL)
}

// Patch starts a PATCH request.
func (c *Client) Patch(ctx context.Context, rawURL string) *Request {
	return c.NewRequest(ctx, http.MethodPatch, rawURL)
}

// Delete starts a DELETE request.
func (c *Client) Delete(ctx context.Context, rawURL string) *Request {
	return c.NewRequest(ctx, http.MethodDelete, rawURL)
}

// Head starts a HEAD request.
func (c *Client) Head(ctx context.Context, rawURL string) *Request {
	return c.NewRequest(ctx, http.MethodHead, rawURL)
}

// Do sends req using the underlying http.Client.
//
// It has the same contract as http.Client.Do: it returns a non-nil response
// only on success, 4xx and 5xx responses are not transport errors, and the
// caller must close a successful response body.
//
// Do does not apply BaseURL or default headers, and it does not modify req.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	if c == nil {
		return nil, errors.New("req: nil client")
	}
	if c.err != nil {
		return nil, c.err
	}
	if req == nil {
		return nil, errors.New("req: nil HTTP request")
	}
	if c.httpClient == nil {
		return nil, configError("client has no underlying HTTP client")
	}
	if enabled, ok := debugContextValue(req.Context()); ok {
		if enabled {
			return c.doDebug(req)
		}
		return c.clientFor(req).Do(req)
	}
	if c.debug.enabled {
		return c.doDebug(req)
	}
	return c.clientFor(req).Do(req)
}

// noRedirectContextKey carries a builder-level NoRedirect through Build and
// Client.Do.
type noRedirectContextKey struct{}

// clientFor returns the http.Client used to send req. A request built with
// NoRedirect uses a shallow copy that stops at the first redirect response,
// so the shared client, its Transport, and its Jar are left untouched.
func (c *Client) clientFor(req *http.Request) *http.Client {
	if noRedirect, _ := req.Context().Value(noRedirectContextKey{}).(bool); !noRedirect {
		return c.httpClient
	}
	hc := *c.httpClient
	hc.CheckRedirect = stopRedirect
	return &hc
}

func stopRedirect(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}
