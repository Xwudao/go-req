package req

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func BenchmarkBuild(b *testing.B) {
	ctx := context.Background()
	b.Run("net-http", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := http.NewRequestWithContext(ctx, "GET", "https://example.test/users?page=1", nil); err != nil {
				b.Fatal(err)
			}
		}
	})
	c := New()
	defer c.CloseIdleConnections()
	b.Run("req", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := c.Get(ctx, "https://example.test/users").Query("page", "1").Build(); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("req-json", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			raw, err := c.Post(ctx, "https://example.test/users").JSON(map[string]string{"name": "Alice"}).Build()
			if err != nil {
				b.Fatal(err)
			}
			raw.Body.Close()
		}
	})
}

func BenchmarkResponse(b *testing.B) {
	transport := featureRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"name":"Alice"}`)), Request: r}, nil
	})
	for _, debug := range []bool{false, true} {
		name := "normal"
		if debug {
			name = "debug"
		}
		b.Run(name, func(b *testing.B) {
			c := New(WithTransport(transport), WithDebug(debug), WithDebugWriter(io.Discard))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var v struct{ Name string }
				if err := c.Get(context.Background(), "https://example.test").ToJSON(&v); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
