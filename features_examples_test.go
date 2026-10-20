package req_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"

	"github.com/Xwudao/go-req"
)

func ExampleRequest_Multipart() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reader, err := r.MultipartReader()
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			data, err := io.ReadAll(part)
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			fmt.Fprintf(w, "%s=%s\n", part.FormName(), data)
		}
	}))
	defer server.Close()
	result, err := req.New().Post(context.Background(), server.URL).Multipart(
		url.Values{"label": {"report"}},
		req.MultipartFile{FieldName: "file", FileName: "report.txt", Reader: strings.NewReader("hello")},
	).ToString()
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Print(result)
	// Output:
	// label=report
	// file=hello
}

func ExampleRequest_ToWriter() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "hello") }))
	defer server.Close()
	var dst bytes.Buffer
	n, err := req.New().Get(context.Background(), server.URL).ToWriter(&dst)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(n, dst.String())
	// Output: 5 hello
}

func ExampleRequest_UseNumber() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"id":9007199254740993}`) }))
	defer server.Close()
	var data map[string]any
	err := req.New().Get(context.Background(), server.URL).MaxResponseBytes(1024).UseNumber(true).ToJSON(&data)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Printf("%T %v\n", data["id"], data["id"])
	// Output: json.Number 9007199254740993
}
