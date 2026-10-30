//go:build ftransport_http2_stdwrap

package ftransport

import (
	"errors"
	"testing"
)

func TestRejectUnsupportedStreamBackend(t *testing.T) {
	_, err := NewTransport(Config{Protocols: []Protocol{ProtocolHTTP2}, HTTP2: HTTP2Config{FirstStreamID: 3}})
	var unsupported *UnsupportedFeatureError
	if !errors.As(err, &unsupported) {
		t.Fatalf("unsupported backend: %v", err)
	}
}
