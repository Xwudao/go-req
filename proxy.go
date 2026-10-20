package req

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// WithProxy configures a fixed proxy for the Client.
//
// rawURL may use the http, https, socks5, or socks5h scheme and may contain
// username and password information. HTTP and HTTPS proxies use the standard
// library's default ports when the URL omits a port; SOCKS5 proxies require an
// explicit port.
//
// Proxy support is delegated entirely to http.Transport.Proxy; req does not
// implement any proxy protocol itself. The proxy is applied once, when the
// Client is initialized, and never modified by a Request builder.
//
// WithProxy cannot be combined with WithHTTPClient, or with WithTransport when
// the supplied RoundTripper is not an *http.Transport.
func WithProxy(rawURL string) Option {
	return func(c *Client) error {
		u, err := parseProxyURL(rawURL)
		if err != nil {
			return err
		}
		c.setProxy(http.ProxyURL(u), "WithProxy")
		return nil
	}
}

// WithoutProxy disables all proxies, including proxies configured through
// HTTP_PROXY, HTTPS_PROXY, and NO_PROXY.
//
// It sets http.Transport.Proxy to nil on req's own transport. It cannot be
// combined with WithHTTPClient, or with WithTransport when the supplied
// RoundTripper is not an *http.Transport.
func WithoutProxy() Option {
	return func(c *Client) error {
		c.setProxy(nil, "WithoutProxy")
		return nil
	}
}

// WithProxyFromEnvironment restores the standard library's environment-based
// proxy selection, http.ProxyFromEnvironment.
//
// This is the default behavior. The option is useful after WithoutProxy or
// when configuring an *http.Transport through WithTransport. It cannot be
// combined with WithHTTPClient, or with WithTransport when the supplied
// RoundTripper is not an *http.Transport.
func WithProxyFromEnvironment() Option {
	return func(c *Client) error {
		c.setProxy(http.ProxyFromEnvironment, "WithProxyFromEnvironment")
		return nil
	}
}

// WithProxyFunc configures proxy selection with a standard library proxy
// function.
//
// The function is called by http.Transport for each connection attempt, so it
// may choose a proxy based on the request, or return nil to connect directly.
// The function must not be nil, and its results should follow the same contract
// as http.Transport.Proxy.
//
// WithProxyFunc is applied once, when the Client is initialized. It cannot be
// combined with WithHTTPClient, or with WithTransport when the supplied
// RoundTripper is not an *http.Transport.
func WithProxyFunc(fn func(*http.Request) (*url.URL, error)) Option {
	return func(c *Client) error {
		if fn == nil {
			return configError("proxy function must not be nil")
		}
		c.setProxy(fn, "WithProxyFunc")
		return nil
	}
}

func (c *Client) setProxy(fn func(*http.Request) (*url.URL, error), option string) {
	c.proxyFunc = fn
	c.proxyOption = option
	c.hasProxyConfig = true
}

func parseProxyURL(raw string) (*url.URL, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, configError("proxy URL must not be empty")
	}

	u, err := url.Parse(raw)
	if err != nil {
		// Do not include the raw URL or the original error text: either may
		// contain proxy credentials. url.Error.Err is the underlying parser
		// reason and does not include userinfo.
		var urlErr *url.Error
		if errors.As(err, &urlErr) && urlErr.Err != nil {
			err = urlErr.Err
		}
		return nil, wrapConfig(err, "invalid proxy URL")
	}

	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, configError(
			"proxy URL scheme %q is not supported; use http, https, socks5, or socks5h",
			u.Scheme,
		)
	}

	if u.Opaque != "" || u.Host == "" || u.Hostname() == "" {
		return nil, configError("proxy URL must be absolute and include a host")
	}
	if strings.HasSuffix(u.Host, ":") {
		return nil, configError("proxy URL contains an empty port")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" {
		return nil, configError("proxy URL must not contain a query or fragment")
	}
	if u.Path != "" && u.Path != "/" {
		return nil, configError("proxy URL must not contain a path")
	}

	port := u.Port()
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, configError("proxy URL port %q must be between 1 and 65535", port)
		}
	} else if scheme == "socks5" || scheme == "socks5h" {
		return nil, configError("proxy URL with scheme %q must include a port", scheme)
	}

	return u, nil
}
