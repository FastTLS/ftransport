package ftransport

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

func TestReorderHTTP1HeaderBlockPreservesValuesAndSelectedCasing(t *testing.T) {
	input := []byte("GET / HTTP/1.1\r\nX-Other: last\r\naccept: one\r\nAccept: two\r\nHost: example.test\r\n\r\n")
	want := []byte("GET / HTTP/1.1\r\nHost: example.test\r\nACCEPT: one\r\nACCEPT: two\r\nX-Other: last\r\n\r\n")

	got, err := reorderH1HeaderBlock(input, []string{"Host", "ACCEPT"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("reordered header:\n%s\nwant:\n%s", got, want)
	}
	if !bytes.Equal(input, []byte("GET / HTTP/1.1\r\nX-Other: last\r\naccept: one\r\nAccept: two\r\nHost: example.test\r\n\r\n")) {
		t.Fatal("input header block was modified")
	}
}

func TestOrderedHTTP1ConnPreservesBodyWrittenWithHeaders(t *testing.T) {
	client, peer := net.Pipe()
	defer client.Close()
	defer peer.Close()
	conn := &orderedH1Conn{
		Conn:           client,
		awaitingHeader: true,
		order:          []string{"Host", "X-First"},
	}
	input := []byte("POST / HTTP/1.1\r\nX-First: one\r\nHost: example.test\r\nContent-Length: 7\r\n\r\npayload")
	want := []byte("POST / HTTP/1.1\r\nHost: example.test\r\nX-First: one\r\nContent-Length: 7\r\n\r\npayload")
	type writeResult struct {
		n   int
		err error
	}
	done := make(chan writeResult, 1)
	go func() {
		n, err := conn.Write(input)
		done <- writeResult{n: n, err: err}
	}()

	if err := peer.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(want))
	if _, err := io.ReadFull(peer, got); err != nil {
		t.Fatal(err)
	}
	result := <-done
	if result.err != nil || result.n != len(input) {
		t.Fatalf("write result = (%d, %v), want (%d, nil)", result.n, result.err, len(input))
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("wire bytes:\n%s\nwant:\n%s", got, want)
	}
}
