package probe

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestOrderedH1ProbeThroughHTTPClient(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	rawRequest := make(chan string, 1)
	serverErr := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverErr <- acceptErr
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		reader := bufio.NewReader(conn)
		var wire strings.Builder
		for {
			line, readErr := reader.ReadString('\n')
			if readErr != nil {
				serverErr <- readErr
				return
			}
			wire.WriteString(line)
			if line == "\r\n" {
				break
			}
		}
		rawRequest <- wire.String()
		_, writeErr := io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
		serverErr <- writeErr
	}()

	fields := []HeaderField{
		{Name: "X-First", Value: "one"},
		{Name: "user-agent", Value: "ftransport-f0"},
		{Name: "X-Last", Value: "three"},
		{Name: "Connection", Value: "close"},
	}
	client := &http.Client{Transport: orderedRoundTripper{fields: fields}, Timeout: 5 * time.Second}
	url := "http://" + listener.Addr().String() + "/probe?q=ordered"
	response, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if string(body) != "ok" {
		t.Fatalf("response body = %q", body)
	}

	expected := "GET /probe?q=ordered HTTP/1.1\r\nHost: " + listener.Addr().String() + "\r\n" +
		"X-First: one\r\n" +
		"user-agent: ftransport-f0\r\n" +
		"X-Last: three\r\n" +
		"Connection: close\r\n\r\n"
	select {
	case got := <-rawRequest:
		if got != expected {
			t.Fatalf("wire request mismatch\nwant: %q\n got: %q", expected, got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for wire request")
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

type orderedRoundTripper struct {
	fields []HeaderField
}

func (rt orderedRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "http" {
		return nil, fmt.Errorf("probe: unsupported scheme %q", req.URL.Scheme)
	}
	conn, err := (&net.Dialer{}).DialContext(req.Context(), "tcp", req.URL.Host)
	if err != nil {
		return nil, err
	}
	target := req.URL.RequestURI()
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	if err := WriteOrderedH1Request(conn, req.Method, target, host, rt.fields); err != nil {
		conn.Close()
		return nil, err
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		conn.Close()
		return nil, err
	}
	response.Body = &connectionBody{ReadCloser: response.Body, conn: conn}
	return response, nil
}

type connectionBody struct {
	io.ReadCloser
	conn net.Conn
}

func (body *connectionBody) Close() error {
	bodyErr := body.ReadCloser.Close()
	connErr := body.conn.Close()
	if bodyErr != nil {
		return bodyErr
	}
	return connErr
}

var _ http.RoundTripper = orderedRoundTripper{}
