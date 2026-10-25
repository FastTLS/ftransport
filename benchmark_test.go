package ftransport

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func BenchmarkReorderHTTP1HeaderBlock(b *testing.B) {
	block := []byte("GET /resource HTTP/1.1\r\nHost: example.test\r\nUser-Agent: benchmark\r\nAccept: */*\r\nAccept-Encoding: gzip\r\nAccept-Language: en-US\r\nX-Request-ID: 0123456789\r\n\r\n")
	order := []string{"Host", "User-Agent", "Accept", "Accept-Encoding", "Accept-Language", "X-Request-ID"}
	b.ReportAllocs()
	for b.Loop() {
		result, err := reorderH1HeaderBlock(block, order)
		if err != nil {
			b.Fatal(err)
		}
		if len(result) != len(block) {
			b.Fatalf("result length = %d, want %d", len(result), len(block))
		}
	}
}

func BenchmarkHTTP1RoundTripWithRequestOptions(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Length", "2")
		_, _ = io.WriteString(response, "ok")
	}))
	defer server.Close()

	transport, err := NewTransport(Config{
		Protocols: []Protocol{ProtocolHTTP1},
		HTTP1: HTTP1Config{HeaderOrder: []string{
			"Host", "Connection", "User-Agent", "Accept", "Accept-Encoding", "Accept-Language",
		}},
	})
	if err != nil {
		b.Fatal(err)
	}
	defer transport.Close()
	client := &http.Client{Transport: transport}
	request, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		b.Fatal(err)
	}
	request.Header.Set("User-Agent", "benchmark")
	request.Header.Set("Accept", "*/*")
	request.Header.Set("Accept-Language", "en-US")
	request, err = WithRequestOptions(request, RequestOptions{HeaderOrder: []string{
		"Host", "Connection", "User-Agent", "Accept", "Accept-Encoding", "Accept-Language",
	}})
	if err != nil {
		b.Fatal(err)
	}

	response, err := client.Do(request)
	if err != nil {
		b.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		response, err := client.Do(request)
		if err != nil {
			b.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		if err := response.Body.Close(); err != nil {
			b.Fatal(err)
		}
	}
}
