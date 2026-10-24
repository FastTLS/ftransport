package probe

import (
	"context"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FastTLS/ftransport/internal/xnet/http2"
	utls "github.com/refraction-networking/utls"
)

func TestUTLSALPNConnectionHandoffAndH2Reuse(t *testing.T) {
	t.Parallel()

	var observationsMu sync.Mutex
	var protocols []string
	var remoteAddresses []string
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		observationsMu.Lock()
		protocols = append(protocols, req.Proto)
		remoteAddresses = append(remoteAddresses, req.RemoteAddr)
		observationsMu.Unlock()
		_, _ = fmt.Fprintf(w, "path=%s", req.URL.Path)
	}))
	server.EnableHTTP2 = true
	counter := &countingListener{Listener: server.Listener}
	server.Listener = counter
	server.StartTLS()
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	roots := x509.NewCertPool()
	certificate := server.Certificate()
	roots.AddCert(certificate)
	serverName := ""
	if len(certificate.DNSNames) != 0 {
		serverName = certificate.DNSNames[0]
	} else if len(certificate.IPAddresses) != 0 {
		serverName = certificate.IPAddresses[0].String()
	} else {
		raw.Close()
		t.Fatal("test server certificate has no usable identity")
	}
	tlsConn := utls.UClient(raw, &utls.Config{
		RootCAs:    roots,
		ServerName: serverName,
		NextProtos: []string{"h2", "http/1.1"},
	}, utls.HelloGolang)
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		tlsConn.Close()
		t.Fatal(err)
	}
	if got := tlsConn.ConnectionState().NegotiatedProtocol; got != "h2" {
		tlsConn.Close()
		t.Fatalf("negotiated ALPN = %q, want h2", got)
	}

	h2Transport := &http2.Transport{}
	clientConn, err := h2Transport.NewClientConn(tlsConn)
	if err != nil {
		tlsConn.Close()
		t.Fatal(err)
	}
	defer clientConn.Close()

	for _, path := range []string{"/first", "/second"} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := clientConn.RoundTrip(req)
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
		if want := "path=" + path; string(body) != want {
			t.Fatalf("body = %q, want %q", body, want)
		}
	}

	if got := counter.accepted.Load(); got != 1 {
		t.Fatalf("accepted connections = %d, want 1", got)
	}
	observationsMu.Lock()
	defer observationsMu.Unlock()
	if len(protocols) != 2 || protocols[0] != "HTTP/2.0" || protocols[1] != "HTTP/2.0" {
		t.Fatalf("server protocols = %#v", protocols)
	}
	if len(remoteAddresses) != 2 || remoteAddresses[0] != remoteAddresses[1] {
		t.Fatalf("requests did not reuse one connection: %#v", remoteAddresses)
	}
}

type countingListener struct {
	net.Listener
	accepted atomic.Int64
}

func (listener *countingListener) Accept() (net.Conn, error) {
	conn, err := listener.Listener.Accept()
	if err == nil {
		listener.accepted.Add(1)
	}
	return conn, err
}
