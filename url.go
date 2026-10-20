package req

import (
	"fmt"
	"net/url"
	"path"
	"strings"
)

// resolveURL resolves raw against base.
//
// An absolute raw URL is returned unchanged. Otherwise its path is appended to
// base's path prefix, whether or not it starts with a slash. Dot segments are
// cleaned using path.Clean. Escaped paths are kept escaped while joining so
// that an encoded slash is not confused with a path separator.
func resolveURL(base *url.URL, raw string) (*url.URL, error) {
	ref, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("req: parse URL %q: %w", raw, err)
	}
	if ref.IsAbs() {
		return ref, nil
	}
	if base == nil {
		return nil, fmt.Errorf("%w: %q", ErrNoBaseURL, raw)
	}

	// A scheme-relative URL replaces the host. RFC 3986 still requires a
	// scheme, so inherit it from the base URL.
	if ref.Host != "" {
		out := *ref
		out.Scheme = base.Scheme
		return &out, nil
	}

	out := *base
	if ref.RawQuery != "" || ref.ForceQuery {
		out.RawQuery = ref.RawQuery
		out.ForceQuery = ref.ForceQuery
	}
	out.Fragment = ref.Fragment
	out.RawFragment = ref.RawFragment

	if ref.Path != "" {
		decodedPath, rawPath, err := joinURLPath(base.EscapedPath(), ref.EscapedPath())
		if err != nil {
			return nil, err
		}
		out.Path = decodedPath
		out.RawPath = rawPath
	}
	return &out, nil
}

func joinURLPath(basePath, refPath string) (decoded, raw string, err error) {
	if refPath == "" {
		return decodeURLPath(basePath)
	}
	if refPath == "/" {
		return decodeURLPath(strings.TrimSuffix(basePath, "/") + "/")
	}

	joined := strings.TrimSuffix(basePath, "/") + "/" + strings.TrimPrefix(refPath, "/")
	trailingSlash := strings.HasSuffix(refPath, "/")
	joined = path.Clean(joined)
	if trailingSlash && !strings.HasSuffix(joined, "/") {
		joined += "/"
	}
	return decodeURLPath(joined)
}

func decodeURLPath(escaped string) (decoded, raw string, err error) {
	if escaped == "" {
		return "", "", nil
	}
	decoded, err = url.PathUnescape(escaped)
	if err != nil {
		return "", "", fmt.Errorf("req: decode URL path: %w", err)
	}
	if decoded != escaped {
		raw = escaped
	}
	return decoded, raw, nil
}
