package req_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/Xwudao/go-req"
)

func ExampleClient_Get() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":1,"name":"Alice"}`)
	}))
	defer srv.Close()

	client := req.New(req.WithBaseURL(srv.URL))

	var user struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	if err := client.Get(context.Background(), "/users/1").ToJSON(&user); err != nil {
		fmt.Println("error:", err)
		return
	}

	fmt.Println(user.Name)
	// Output: Alice
}

func ExampleClient_Post() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":7}`)
	}))
	defer srv.Close()

	client := req.New(req.WithBaseURL(srv.URL))

	var created struct {
		ID int `json:"id"`
	}
	err := client.Post(context.Background(), "/users").
		JSON(map[string]any{"name": "Alice", "age": 25}).
		ToJSON(&created)
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	fmt.Println(created.ID)
	// Output: 7
}

func ExampleClient_Do() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "hello")
	}))
	defer srv.Close()

	client := req.New()
	raw, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/download", nil)
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	resp, err := client.Do(raw)
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	defer resp.Body.Close()

	n, err := io.Copy(io.Discard, resp.Body)
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	fmt.Println(n)
	// Output: 5
}

func ExampleWithDebug() {
	var debug bytes.Buffer
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "pong")
	}))
	defer srv.Close()

	client := req.New(
		req.WithDebug(true),
		req.WithDebugWriter(&debug),
		req.WithBaseURL(srv.URL),
	)
	if _, err := client.Get(context.Background(), "/ping").ToString(); err != nil {
		fmt.Println("error:", err)
		return
	}

	output := debug.String()
	fmt.Println(strings.Contains(output, "GET ") && strings.Contains(output, "Status: 200 OK"))
	// Output: true
}
