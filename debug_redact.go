package req

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// WithDebugRedactHeaders adds case-insensitive header names to redact.
// Repeated calls accumulate names. Built-in redaction remains enabled.
func WithDebugRedactHeaders(names ...string) Option {
	names = append([]string(nil), names...)
	return func(c *Client) error { c.debug.redactHeaders = append(c.debug.redactHeaders, names...); return nil }
}

// WithDebugRedactQuery adds case-insensitive query parameter names to redact.
func WithDebugRedactQuery(names ...string) Option {
	names = append([]string(nil), names...)
	return func(c *Client) error { c.debug.redactQuery = append(c.debug.redactQuery, names...); return nil }
}

// WithDebugRedactJSONFields redacts matching keys at every depth of JSON bodies.
// Names are case-insensitive. When configured, incomplete or invalid JSON is
// omitted entirely, rather than risking disclosure from truncated captures.
// Non-JSON bodies are unaffected; disable body logging for other sensitive data.
func WithDebugRedactJSONFields(names ...string) Option {
	names = append([]string(nil), names...)
	return func(c *Client) error { c.debug.redactJSON = append(c.debug.redactJSON, names...); return nil }
}

func matchesDebugName(key string, names []string) bool {
	for _, name := range names {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}
func redactConfiguredHeaders(h http.Header, cfg debugConfig) http.Header {
	out := h.Clone()
	for key := range out {
		if matchesDebugName(key, cfg.redactHeaders) {
			out[key] = []string{"[REDACTED]"}
		}
	}
	return out
}
func redactConfiguredRequest(req *http.Request, cfg debugConfig) *http.Request {
	out := new(http.Request)
	*out = *req
	out.Header = redactConfiguredHeaders(req.Header, cfg)
	if req.URL != nil {
		u := *req.URL
		parts := strings.Split(u.RawQuery, "&")
		for i, part := range parts {
			key, _, _ := strings.Cut(part, "=")
			decoded, err := url.QueryUnescape(key)
			if err == nil && matchesDebugName(decoded, cfg.redactQuery) {
				parts[i] = key + "=REDACTED"
			}
		}
		u.RawQuery = strings.Join(parts, "&")
		out.URL = &u
	}
	return out
}
func redactConfiguredResponse(resp *http.Response, cfg debugConfig) *http.Response {
	out := new(http.Response)
	*out = *resp
	out.Header = redactConfiguredHeaders(resp.Header, cfg)
	return out
}
func redactConfiguredError(err error, cfg debugConfig) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		copyErr := *ue
		if u, parseErr := url.Parse(ue.URL); parseErr == nil {
			copyErr.URL = redactConfiguredRequest(&http.Request{URL: u}, cfg).URL.String()
		}
		copyErr.Err = redactConfiguredError(ue.Err, cfg)
		return &copyErr
	}
	return err
}
func redactConfiguredBody(contentType string, snapshot debugBodySnapshot, cfg debugConfig) debugBodySnapshot {
	if len(cfg.redactJSON) == 0 || !strings.Contains(strings.ToLower(contentType), "json") {
		return snapshot
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(snapshot.Data))
	decoder.UseNumber()
	if !snapshot.Complete || snapshot.Truncated || !json.Valid(snapshot.Data) || decoder.Decode(&value) != nil {
		snapshot.Data = []byte("[JSON body omitted: incomplete or invalid]")
		return snapshot
	}
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			for key, child := range v {
				if matchesDebugName(key, cfg.redactJSON) {
					v[key] = "[REDACTED]"
				} else {
					walk(child)
				}
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(value)
	snapshot.Data, _ = json.Marshal(value)
	return snapshot
}
