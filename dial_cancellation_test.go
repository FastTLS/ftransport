package ftransport

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The peer deliberately never reads: even the small HTTP/2 preface blocks.
// Observing Write return proves the dial goroutine stopped, rather than just
// the request's wait for a pooled connection being abandoned.
type stalledPrefaceConn struct {
	net.Conn
	started, stopped chan struct{}
	once             sync.Once
}

type cancelledDialUpload struct {
	once   sync.Once
	closed chan struct{}
}

func (b *cancelledDialUpload) Read([]byte) (int, error) { <-b.closed; return 0, io.EOF }
func (b *cancelledDialUpload) Close() error             { b.once.Do(func() { close(b.closed) }); return nil }

func (c *stalledPrefaceConn) Write(p []byte) (int, error) {
	c.once.Do(func() { close(c.started) })
	n, err := c.Conn.Write(p)
	close(c.stopped)
	return n, err
}

func awaitDialEvent(t *testing.T, event <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-event:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s did not complete", name)
	}
}

func TestHTTP2PrefaceCancellationReleasesDial(t *testing.T) {
	for _, action := range []string{"cancel", "deadline", "transport-close"} {
		t.Run(action, func(t *testing.T) {
			clientConn, peer := net.Pipe()
			defer clientConn.Close()
			defer peer.Close()
			conn := &stalledPrefaceConn{Conn: clientConn, started: make(chan struct{}), stopped: make(chan struct{})}
			transport, err := NewTransport(Config{
				Protocols: []Protocol{ProtocolHTTP2},
				DialContext: func(context.Context, DialRequest) (DialResult, error) {
					return DialResult{Conn: conn, TLSState: &tls.ConnectionState{
						HandshakeComplete: true, NegotiatedProtocol: "h2", NegotiatedProtocolIsMutual: true,
					}}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer transport.Close()
			ctx, cancel := context.WithCancel(context.Background())
			if action == "deadline" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 300*time.Millisecond)
			}
			defer cancel()
			upload := &cancelledDialUpload{closed: make(chan struct{})}
			defer upload.Close()
			request, _ := http.NewRequestWithContext(ctx, "POST", "https://preface.invalid/", upload)
			result := make(chan error, 1)
			go func() { _, err := transport.RoundTrip(request); result <- err }()
			awaitDialEvent(t, conn.started, "initial frame write")
			want := error(context.Canceled)
			switch action {
			case "cancel":
				cancel()
			case "deadline":
				want = context.DeadlineExceeded
			case "transport-close":
				want = ErrTransportClosed
				transport.Close()
			}
			select {
			case err := <-result:
				if !errors.Is(err, want) {
					t.Fatalf("request error = %v, want %v", err, want)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("request remained blocked")
			}
			awaitDialEvent(t, conn.stopped, "cancelled initial frame write")
			awaitDialEvent(t, upload.closed, "abandoned upload close")
			waitFor(t, time.Second, func() bool { return transport.Stats().Active == 0 })
		})
	}
}

func TestCustomHTTP2IdleLimitsRejectedBeforeDial(t *testing.T) {
	for _, pool := range []PoolConfig{{MaxIdleConnections: 1}, {MaxIdlePerHost: 1}} {
		var dials atomic.Int32
		transport, err := NewTransport(Config{
			Protocols: []Protocol{ProtocolHTTP2}, Pool: pool,
			DialContext: func(context.Context, DialRequest) (DialResult, error) {
				dials.Add(1)
				return DialResult{}, errors.New("unexpected dial")
			},
		})
		if transport != nil {
			transport.Close()
			t.Fatal("unsupported limit accepted")
		}
		var unsupported *UnsupportedFeatureError
		if !errors.Is(err, ErrUnsupportedFeature) || !errors.As(err, &unsupported) {
			t.Fatalf("wrong limit error: %v", err)
		}
		if dials.Load() != 0 {
			t.Fatal("unsupported limit caused network activity")
		}
	}
}

func TestHTTP2WaiterRecoversAfterCancelledPreface(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			t.Error("expected HTTP/2")
		}
		io.WriteString(w, "recovered")
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	firstConn, peer := net.Pipe()
	defer firstConn.Close()
	defer peer.Close()
	stalled := &stalledPrefaceConn{Conn: firstConn, started: make(chan struct{}), stopped: make(chan struct{})}
	var calls atomic.Int32
	transport, err := NewTransport(Config{
		Protocols: []Protocol{ProtocolHTTP2},
		DialContext: func(ctx context.Context, req DialRequest) (DialResult, error) {
			if calls.Add(1) == 1 {
				return DialResult{Conn: stalled, TLSState: &tls.ConnectionState{
					HandshakeComplete: true, NegotiatedProtocol: "h2", NegotiatedProtocolIsMutual: true,
				}}, nil
			}
			dialer := tls.Dialer{Config: &tls.Config{RootCAs: server.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs, NextProtos: []string{"h2"}}}
			conn, err := dialer.DialContext(ctx, req.Network, req.Address)
			if err != nil {
				return DialResult{}, err
			}
			state := conn.(*tls.Conn).ConnectionState()
			return DialResult{Conn: conn, TLSState: &state}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first, _ := http.NewRequestWithContext(ctx, "GET", server.URL, nil)
	firstResult := make(chan error, 1)
	go func() { _, err := transport.RoundTrip(first); firstResult <- err }()
	awaitDialEvent(t, stalled.started, "first preface")
	secondCtx, secondCancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer secondCancel()
	second, _ := http.NewRequestWithContext(secondCtx, "GET", server.URL, nil)
	secondResult := make(chan error, 1)
	go func() {
		response, err := transport.RoundTrip(second)
		if err == nil {
			data, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			err = readErr
			if string(data) != "recovered" {
				err = errors.New("incorrect response")
			}
		}
		secondResult <- err
	}()
	waitFor(t, time.Second, func() bool { return transport.Stats().RoundTrips == 2 })
	cancel()
	if err := <-firstResult; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	select {
	case err := <-secondResult:
		if err != nil {
			t.Fatalf("live waiter could not recover: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("live waiter remained blocked")
	}
	awaitDialEvent(t, stalled.stopped, "first dial release")
	if calls.Load() != 2 {
		t.Fatalf("dial calls = %d, want 2", calls.Load())
	}
}
