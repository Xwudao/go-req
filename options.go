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

// WithErrorCheck sets the ErrorCheck applied to responses read by requests
// created by the Client. Use it for APIs that report errors in the response
// body, including with a 2xx status; JSONError builds a check for a JSON error
// type. Request.ErrorCheck overrides it for a single request.
//
// check must not be nil.
func WithErrorCheck(check ErrorCheck) Option {
	return func(c *Client) error {
		if check == nil {
			return configError("error check must not be nil")
		}
		c.errorCheck = check
		return nil
	}
}

// WithQuery sets a default query parameter for requests created by the Client.
//
// A default is added only when the request URL does not already contain key;
// Query, AddQuery, and QueryValues on the builder are applied afterwards and
// can override or extend it. WithQuery may be repeated; the last value for
// the same key wins.
func WithQuery(key, value string) Option {
	return func(c *Client) error {
		if c.query == nil {
			c.query = make(url.Values)
		}
		c.query.Set(key, value)
		return nil
	}
}

// WithBeforeRequest adds a hook that runs on every *http.Request built by the
// Client's request builders, after the URL, query, headers, and body are set.
//
// Hooks run in the order they were added, followed by hooks added with
// Request.BeforeRequest. They may modify the request, for example to add a
// signature or a fresh access token. Returning an error aborts the request.
// To read a JSON or Form body without consuming it, use req.GetBody. Hooks
// are not applied to requests passed directly to Client.Do.
func WithBeforeRequest(hook func(*http.Request) error) Option {
	return func(c *Client) error {
		if hook == nil {
			return configError("before-request hook must not be nil")
		}
		c.beforeRequest = append(c.beforeRequest, hook)
		return nil
	}
}

// WithoutRedirect makes the Client return redirect responses instead of
// following them. Use Request.NoRedirect to do this for a single request.
//
// It cannot be combined with WithHTTPClient; set http.Client.CheckRedirect on
// the supplied client instead.
func WithoutRedirect() Option {
	return func(c *Client) error {
		c.checkRedirect = stopRedirect
		c.redirectOption = "WithoutRedirect"
		return nil
	}
}

// WithCheckRedirect sets the redirect policy of the http.Client created by
// req, with the same contract as http.Client.CheckRedirect. For example, it
// can limit the number of redirects or refuse to leave a host. Returning
// http.ErrUseLastResponse stops at the current redirect response.
//
// It cannot be combined with WithHTTPClient. The last of WithCheckRedirect and
// WithoutRedirect wins.
func WithCheckRedirect(fn func(req *http.Request, via []*http.Request) error) Option {
	return func(c *Client) error {
		if fn == nil {
			return configError("redirect policy must not be nil")
		}
		c.checkRedirect = fn
		c.redirectOption = "WithCheckRedirect"
		return nil
	}
}
