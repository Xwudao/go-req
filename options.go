package req

import (
	"context"
	"math"
	"net"
	"net/http"
	"net/url"
	"time"
)

// Option configures a Client.
//
// Options are applied in argument order. Options that set a single value
// (BaseURL, timeout, HTTP client, transport, dial function, proxy selection, and
// response size limit) are last-one-wins. WithHeader accumulates headers.
//
// Incompatible combinations are detected after every option has been applied,
// so the result does not depend on option order. In particular, WithHTTPClient
// cannot be combined with WithTimeout, WithTransport, WithDialContext, or any
// proxy option, and WithTransport cannot be combined with WithDialContext.
// Proxy options combined with WithTransport require the supplied RoundTripper
// to be an *http.Transport so it can be cloned safely. Use NewWithOptions to
// handle these errors explicitly.
type Option func(*Client) error

// WithBaseURL sets the base URL used to resolve relative request URLs.
//
// raw must be an absolute URL with a scheme and host. It must not contain a
// query or fragment, because silently combining those with request URLs would
// be ambiguous.
//
// A request URL with its own scheme and host is used as-is and ignores the base
// URL. Other request URLs are appended to the base URL's path prefix: both
// "users" and "/users" resolve to "<base-path>/users".
func WithBaseURL(raw string) Option {
	return func(c *Client) error {
		u, err := url.Parse(raw)
		if err != nil {
			return wrapConfig(err, "invalid base URL %q", raw)
		}
		if !u.IsAbs() || u.Host == "" {
			return configError("base URL %q must be absolute and include a host", raw)
		}
		if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
			return configError("base URL %q must not contain a query or fragment", raw)
		}
		c.baseURL = u
		return nil
	}
}

// WithTimeout sets the timeout of the http.Client created by req.
//
// It cannot be combined with WithHTTPClient, because req never mutates an
// externally supplied http.Client. Set http.Client.Timeout directly when
// supplying your own client.
func WithTimeout(timeout time.Duration) Option {
	return func(c *Client) error {
		if timeout < 0 {
			return configError("timeout must not be negative")
		}
		c.timeout = timeout
		c.hasTimeout = true
		return nil
	}
}

// WithHeader adds a default header to requests created by the Client.
//
// Request-level Header calls override a default header with the same name.
// WithHeader may be repeated; the last value for the same header wins.
func WithHeader(key, value string) Option {
	return func(c *Client) error {
		if c.header == nil {
			c.header = make(http.Header)
		}
		c.header.Set(key, value)
		return nil
	}
}

// WithHTTPClient makes c use the supplied http.Client directly.
//
// The client is not copied or modified, and its Transport, Jar, CheckRedirect,
// and Timeout behavior are all preserved. The caller retains ownership and must
// not mutate the client while it is in use.
//
// WithHTTPClient cannot be combined with WithTimeout, WithTransport,
// WithDialContext, or any proxy option.
func WithHTTPClient(client *http.Client) Option {
	return func(c *Client) error {
		if client == nil {
			return configError("HTTP client must not be nil")
		}
		c.httpClient = client
		c.hasHTTPClient = true
		return nil
	}
}

// WithTransport makes c use the supplied http.RoundTripper.
//
// req wraps the transport in its own http.Client but never changes the
// transport's configuration. The caller retains ownership of the transport.
// WithTransport cannot be combined with WithHTTPClient or WithDialContext.
//
// When combined with a proxy option, transport must be an *http.Transport. req
// clones it, sets Proxy on the clone, and leaves the caller's transport
// untouched.
func WithTransport(transport http.RoundTripper) Option {
	return func(c *Client) error {
		if transport == nil {
			return configError("transport must not be nil")
		}
		c.transport = transport
		c.hasTransport = true
		return nil
	}
}

// WithDialContext sets the dial function used for network connections.
//
// req clones http.DefaultTransport and sets DialContext on the clone. Neither
// http.DefaultTransport nor any caller-supplied transport is modified.
//
// DialContext does not necessarily control every connection path. In
// particular, an HTTP proxy configured through the environment is dialed using
// this function, not the proxy's final destination. Custom transports and
// custom http.Clients are not introspected, which is why WithDialContext cannot
// be combined with WithTransport or WithHTTPClient.
//
// WithDialContext can be combined with proxy options; both are applied to req's
// cloned *http.Transport.
func WithDialContext(dial func(ctx context.Context, network, address string) (net.Conn, error)) Option {
	return func(c *Client) error {
		if dial == nil {
			return configError("dial function must not be nil")
		}
		c.dialContext = dial
		c.hasDialContext = true
		return nil
	}
}

// WithMaxResponseBytes sets the maximum number of response body bytes read by
// ToJSON, ToBytes, and ToString.
//
// n must be positive. The default is DefaultMaxResponseBytes. Do and Build are
// not affected; use them with io.Copy for large downloads.
func WithMaxResponseBytes(n int64) Option {
	return func(c *Client) error {
		if n <= 0 {
			return configError("maximum response size must be positive")
		}
		if n == math.MaxInt64 {
			return configError("maximum response size is too large")
		}
		c.maxResponseBytes = n
		return nil
	}
}
