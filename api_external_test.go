package ftransport_test

import (
	"net/http"
	"testing"

	"github.com/FastTLS/ftransport"
)

func TestStandardHTTPClientConsumerCompiles(t *testing.T) {
	transport, err := ftransport.NewTransport(ftransport.Config{
		Protocols: []ftransport.Protocol{
			ftransport.ProtocolHTTP1,
			ftransport.ProtocolHTTP2,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()

	var roundTripper http.RoundTripper = transport
	client := &http.Client{Transport: roundTripper}
	if client.Transport == nil {
		t.Fatal("transport was not installed")
	}
}
