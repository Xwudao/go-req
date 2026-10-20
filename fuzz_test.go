package req

import (
	"net/url"
	"strings"
	"testing"
)

func FuzzResolveURL(f *testing.F) {
	for _, s := range []string{"users", "../users", "a%2Fb", "//other.test/x", "?a=1", "/a/../b/", "%zz"} {
		f.Add(s)
	}
	base, _ := url.Parse("https://example.test/v1")
	f.Fuzz(func(t *testing.T, raw string) {
		got, err := resolveURL(base, raw)
		if err != nil {
			return
		}
		if got == nil {
			t.Fatal("nil URL without error")
		}
		if _, err := url.Parse(got.String()); err != nil {
			t.Fatalf("invalid resolved URL: %v", err)
		}
		if got.RawPath != "" {
			decoded, err := url.PathUnescape(got.RawPath)
			if err != nil || decoded != got.Path {
				t.Fatal("inconsistent escaped path")
			}
		}
		if base.String() != "https://example.test/v1" {
			t.Fatal("mutated base")
		}
	})
}

func FuzzProxyConfiguration(f *testing.F) {
	for _, s := range []string{"http://localhost:8080", "socks5://user:pass@localhost:1080", "http://host:99999", "/relative", "http://host/path"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		c := New(WithProxy(raw))
		if c.Err() != nil {
			return
		}
		defer c.CloseIdleConnections()
		if c.HTTPClient() == nil {
			t.Fatal("missing HTTP client")
		}
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" {
			t.Fatal("accepted invalid proxy")
		}
	})
}

func FuzzDebugRedaction(f *testing.F) {
	for _, s := range []string{"hello", "a&b=c", "%zz", "秘密", "\r\n"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, value string) {
		const secret = "SENSITIVE_SENTINEL_7d6f"
		u := &url.URL{Scheme: "https", Host: "example.test", User: url.UserPassword("user", secret), RawQuery: url.Values{"token": {secret}, "q": {value}}.Encode(), Fragment: secret}
		got := redactDebugURL(u)
		parsed, err := url.Parse(got)
		if err != nil {
			t.Fatal(err)
		}
		if parsed.Query().Get("token") != "REDACTED" || parsed.Fragment != "REDACTED" {
			t.Fatal("secret not redacted")
		}
		if _, set := parsed.User.Password(); set {
			t.Fatal("password retained")
		}
		if u.Query().Get("token") != secret {
			t.Fatal("input mutated")
		}
		// Sanitization must not allow log header injection.
		if strings.ContainsAny(sanitizeDebugText(value), "\r\n") {
			t.Fatal("newline injection")
		}
	})
}
