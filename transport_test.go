package ftransport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	utls "github.com/refraction-networking/utls"
)

func TestNewTransportAcceptsImplementedWireConfigOffline(t *testing.T) {
	t.Parallel()

	var dials atomic.Int64
	transport, err := NewTransport(Config{
		Protocols: []Protocol{ProtocolHTTP1},
		DialContext: func(context.Context, DialRequest) (DialResult, error) {
			dials.Add(1)
			return DialResult{}, errors.New("must not run")
		},
		HTTP1: HTTP1Config{HeaderOrder: []string{"Accept"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if transport == nil {
		t.Fatal("constructor returned nil transport")
	}
	defer transport.Close()
	if got := dials.Load(); got != 0 {
		t.Fatalf("dial count = %d, want 0", got)
	}
}

func TestHTTP1CancellationClosesUpstreamRequest(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		_, _ = io.Copy(io.Discard, request.Body)
		_ = request.Body.Close()
		close(started)
		select {
		case <-request.Context().Done():
			close(canceled)
		case <-time.After(5 * time.Second):
		}
	}))
	defer server.Close()

	transport, err := NewTransport(Config{Protocols: []Protocol{ProtocolHTTP1}})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	requestDone := make(chan error, 1)
	go func() {
		response, requestErr := transport.RoundTrip(request)
		if response != nil {
			response.Body.Close()
		}
		requestDone <- requestErr
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream request did not start")
	}
	cancel()
	select {
	case err := <-requestDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("round trip error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("round trip did not return after cancellation")
	}
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream request context was not canceled")
	}
}

func TestAmbiguousRequestWireOptionsRejectedBeforeDial(t *testing.T) {
	t.Parallel()

	transport, err := NewTransport(Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	upload := newBlockingUploadBody()
	request, err := http.NewRequest(http.MethodPost, "http://127.0.0.1:1/", upload)
	if err != nil {
		t.Fatal(err)
	}
	request, err = WithRequestOptions(request, RequestOptions{HeaderOrder: []string{"Accept"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = (&http.Client{Transport: transport}).Do(request)
	if !errors.Is(err, ErrUnsupportedFeature) {
		t.Fatalf("error = %v, want ErrUnsupportedFeature", err)
	}
	select {
	case <-upload.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("rejected request body was not closed")
	}
	if stats := transport.Stats(); stats.Dials != 0 || stats.RoundTrips != 0 {
		t.Fatalf("request reached transport internals: %#v", stats)
	}
}

func TestZeroTransportRejectedWithoutPanic(t *testing.T) {
	t.Parallel()

	request, err := http.NewRequest(http.MethodGet, "https://example.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := new(Transport).RoundTrip(request); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error = %v, want ErrInvalidConfig", err)
	}
	if err := new(Transport).Close(); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentCloseIsIdempotent(t *testing.T) {
	t.Parallel()

	transport, err := NewTransport(Config{})
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	for range 32 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if err := transport.Close(); err != nil {
				t.Errorf("Close: %v", err)
			}
		}()
	}
	wait.Wait()
	if !transport.Stats().Closed {
		t.Fatal("transport did not report closed")
	}
}

func TestStandardHTTP1ClientTraceReuseAndStats(t *testing.T) {
	t.Parallel()

	var accepts atomic.Int64
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Accept-Encoding"); got != "" {
			t.Errorf("unexpected automatic Accept-Encoding %q", got)
		}
		_, _ = io.WriteString(response, "ok")
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			accepts.Add(1)
		}
	}
	server.Start()
	defer server.Close()

	transport, err := NewTransport(Config{Protocols: []Protocol{ProtocolHTTP1}})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	client := &http.Client{Transport: transport}

	var getConn, gotConn, wroteRequest atomic.Int64
	for range 2 {
		request, err := http.NewRequest(http.MethodGet, server.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		request = request.WithContext(httptrace.WithClientTrace(request.Context(), &httptrace.ClientTrace{
			GetConn: func(string) { getConn.Add(1) },
			GotConn: func(httptrace.GotConnInfo) { gotConn.Add(1) },
			WroteRequest: func(httptrace.WroteRequestInfo) {
				wroteRequest.Add(1)
			},
		}))
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, response.Body); err != nil {
			t.Fatal(err)
		}
		if err := response.Body.Close(); err != nil {
			t.Fatal(err)
		}
		if response.Request != request {
			t.Fatal("response did not retain the caller request")
		}
		if err := request.Context().Err(); err != nil {
			t.Fatalf("caller request context was changed: %v", err)
		}
	}

	if got := accepts.Load(); got != 1 {
		t.Fatalf("accepted connections = %d, want 1", got)
	}
	if getConn.Load() != 2 || gotConn.Load() != 2 || wroteRequest.Load() != 2 {
		t.Fatalf("trace counts: GetConn=%d GotConn=%d WroteRequest=%d", getConn.Load(), gotConn.Load(), wroteRequest.Load())
	}
	stats := transport.Stats()
	if stats.RoundTrips != 2 || stats.Responses != 2 || stats.Completed != 2 || stats.Active != 0 || stats.Dials != 1 || stats.Failed != 0 {
		t.Fatalf("unexpected stats: %#v", stats)
	}
}

func TestDefaultTransportIgnoresEnvironmentProxyAndVerifiesTLS(t *testing.T) {
	t.Parallel()

	transport, err := NewTransport(Config{Protocols: []Protocol{ProtocolHTTP1}})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	ordered, ok := transport.base.(*orderedH1Transport)
	if !ok {
		t.Fatalf("base type = %T", transport.base)
	}
	base := ordered.base
	if base.Proxy != nil {
		t.Fatal("default transport unexpectedly reads environment proxy settings")
	}
	if !base.DisableCompression {
		t.Fatal("default transport unexpectedly enables transparent compression")
	}

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()
	_, err = (&http.Client{Transport: transport}).Get(server.URL)
	var unknownAuthority x509.UnknownAuthorityError
	if !errors.As(err, &unknownAuthority) {
		t.Fatalf("error = %v, want x509.UnknownAuthorityError", err)
	}
}

func TestStandardHTTP2NegotiationAndReuse(t *testing.T) {
	t.Parallel()

	var accepts atomic.Int64
	var protocolsMu sync.Mutex
	var protocols []string
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		protocolsMu.Lock()
		protocols = append(protocols, request.Proto)
		protocolsMu.Unlock()
		_, _ = io.WriteString(response, request.URL.Path)
	}))
	server.EnableHTTP2 = true
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			accepts.Add(1)
		}
	}
	server.StartTLS()
	defer server.Close()

	transport, err := NewTransport(Config{
		Protocols: []Protocol{ProtocolHTTP2},
		TLSConfig: verifiedTestTLSConfig(t, server),
		Pool:      PoolConfig{MaxConnectionsPerHost: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	client := &http.Client{Transport: transport}

	for _, path := range []string{"/one", "/two"} {
		response, err := client.Get(server.URL + path)
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
		if string(body) != path {
			t.Fatalf("body = %q, want %q", body, path)
		}
		if response.TLS == nil || response.TLS.NegotiatedProtocol != "h2" {
			t.Fatalf("TLS state = %#v, want negotiated h2", response.TLS)
		}
	}
	if got := accepts.Load(); got != 1 {
		t.Fatalf("accepted connections = %d, want 1", got)
	}
	transport.CloseIdleConnections()
	response, err := client.Get(server.URL + "/after-close-idle")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if got := accepts.Load(); got != 2 {
		t.Fatalf("accepted connections after CloseIdleConnections = %d, want 2", got)
	}
	protocolsMu.Lock()
	defer protocolsMu.Unlock()
	if len(protocols) != 3 || protocols[0] != "HTTP/2.0" || protocols[1] != "HTTP/2.0" || protocols[2] != "HTTP/2.0" {
		t.Fatalf("protocols = %#v", protocols)
	}
}

func TestCustomTLSHTTP1HandoffNormalizesResponseState(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(response, request.Proto)
	}))
	defer server.Close()

	roots, serverName := testServerTrust(t, server)
	transport, err := NewTransport(Config{
		Protocols: []Protocol{ProtocolHTTP1},
		DialContext: func(ctx context.Context, request DialRequest) (DialResult, error) {
			if !request.TLS || request.ServerName != "example.com" || len(request.ALPNProtocols) != 1 || request.ALPNProtocols[0] != "http/1.1" {
				return DialResult{}, errors.New("unexpected dial request")
			}
			raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
			if err != nil {
				return DialResult{}, err
			}
			conn := tls.Client(raw, &tls.Config{
				RootCAs:    roots,
				ServerName: serverName,
				NextProtos: request.ALPNProtocols,
			})
			if err := conn.HandshakeContext(ctx); err != nil {
				raw.Close()
				return DialResult{}, err
			}
			state := conn.ConnectionState()
			return DialResult{Conn: conn, NegotiatedProtocol: state.NegotiatedProtocol, TLSState: &state}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()

	response, err := (&http.Client{Transport: transport}).Get("https://example.com/")
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
	if string(body) != "HTTP/1.1" {
		t.Fatalf("body = %q", body)
	}
	if response.TLS == nil || !response.TLS.HandshakeComplete || len(response.TLS.VerifiedChains) == 0 {
		t.Fatalf("normalized TLS state = %#v", response.TLS)
	}
}

func TestCustomUTLSHTTP2HandoffAndReuse(t *testing.T) {
	t.Parallel()

	var accepts atomic.Int64
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(response, request.URL.Path)
	}))
	server.EnableHTTP2 = true
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			accepts.Add(1)
		}
	}
	server.StartTLS()
	defer server.Close()

	roots, serverName := testServerTrust(t, server)
	transport, err := NewTransport(Config{
		Protocols: []Protocol{ProtocolHTTP2},
		DialContext: func(ctx context.Context, request DialRequest) (DialResult, error) {
			if !request.TLS || request.ServerName != "example.com" || len(request.ALPNProtocols) != 1 || request.ALPNProtocols[0] != "h2" {
				return DialResult{}, errors.New("unexpected dial request")
			}
			raw, err := (&net.Dialer{}).DialContext(ctx, request.Network, server.Listener.Addr().String())
			if err != nil {
				return DialResult{}, err
			}
			conn := utls.UClient(raw, &utls.Config{
				RootCAs:    roots,
				ServerName: serverName,
				NextProtos: request.ALPNProtocols,
			}, utls.HelloGolang)
			if err := conn.HandshakeContext(ctx); err != nil {
				raw.Close()
				return DialResult{}, err
			}
			state := normalizeUTLSState(conn.ConnectionState())
			return DialResult{Conn: conn, NegotiatedProtocol: state.NegotiatedProtocol, TLSState: &state}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	client := &http.Client{Transport: transport}

	for _, path := range []string{"/first", "/second"} {
		response, err := client.Get("https://example.com" + path)
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
		if string(body) != path {
			t.Fatalf("body = %q, want %q", body, path)
		}
		if response.TLS == nil || response.TLS.NegotiatedProtocol != "h2" || len(response.TLS.VerifiedChains) == 0 {
			t.Fatalf("TLS state = %#v", response.TLS)
		}
	}
	if got := accepts.Load(); got != 1 {
		t.Fatalf("accepted connections = %d, want 1", got)
	}
	transport.CloseIdleConnections()
	response, err := client.Get("https://example.com/after-close-idle")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if got := accepts.Load(); got != 2 {
		t.Fatalf("accepted connections after CloseIdleConnections = %d, want 2", got)
	}
}

func TestHTTP2CancellationIsolatedFromSiblingStream(t *testing.T) {
	t.Parallel()

	slowStarted := make(chan struct{})
	var startOnce sync.Once
	var accepts atomic.Int64
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/slow" {
			startOnce.Do(func() { close(slowStarted) })
			<-request.Context().Done()
			return
		}
		_, _ = io.WriteString(response, "fast")
	}))
	server.EnableHTTP2 = true
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			accepts.Add(1)
		}
	}
	server.StartTLS()
	defer server.Close()

	transport, err := NewTransport(Config{
		Protocols: []Protocol{ProtocolHTTP2},
		TLSConfig: verifiedTestTLSConfig(t, server),
		HTTP2: HTTP2Config{InitialSettings: ExplicitHTTP2Settings(
			HTTP2Setting{ID: SettingEnablePush, Value: 0},
			HTTP2Setting{ID: SettingInitialWindowSize, Value: 4 << 20},
			HTTP2Setting{ID: SettingMaxFrameSize, Value: 16 << 10},
		)},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	client := &http.Client{Transport: transport}

	slowCtx, cancelSlow := context.WithCancel(context.Background())
	slowRequest, err := http.NewRequestWithContext(slowCtx, http.MethodGet, server.URL+"/slow", nil)
	if err != nil {
		t.Fatal(err)
	}
	slowResult := make(chan error, 1)
	go func() {
		response, requestErr := client.Do(slowRequest)
		if response != nil {
			response.Body.Close()
		}
		slowResult <- requestErr
	}()

	select {
	case <-slowStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("slow request did not start")
	}
	fastResponse, err := client.Get(server.URL + "/fast")
	if err != nil {
		t.Fatal(err)
	}
	fastBody, err := io.ReadAll(fastResponse.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := fastResponse.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if string(fastBody) != "fast" {
		t.Fatalf("fast body = %q", fastBody)
	}
	cancelSlow()
	select {
	case err := <-slowResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("slow error = %v, want context cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("slow request did not cancel")
	}
	if got := accepts.Load(); got != 1 {
		t.Fatalf("accepted connections = %d, want 1", got)
	}
}

func TestHTTP2FullDuplexRemainsActiveUntilBothDirectionsFinish(t *testing.T) {
	t.Parallel()

	headersSent := make(chan struct{})
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.WriteHeader(http.StatusOK)
		response.(http.Flusher).Flush()
		close(headersSent)
		_, _ = io.Copy(io.Discard, request.Body)
		_, _ = io.WriteString(response, "done")
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()

	transport, err := NewTransport(Config{
		Protocols: []Protocol{ProtocolHTTP2},
		TLSConfig: verifiedTestTLSConfig(t, server),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	reader, writer := io.Pipe()
	request, err := http.NewRequest(http.MethodPost, server.URL, reader)
	if err != nil {
		t.Fatal(err)
	}
	responseResult := make(chan *http.Response, 1)
	errorResult := make(chan error, 1)
	go func() {
		response, requestErr := (&http.Client{Transport: transport}).Do(request)
		responseResult <- response
		errorResult <- requestErr
	}()

	select {
	case <-headersSent:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not send response headers")
	}
	var response *http.Response
	select {
	case response = <-responseResult:
		if err := <-errorResult; err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RoundTrip did not return while upload remained open")
	}
	if stats := transport.Stats(); stats.Active != 1 || stats.Responses != 1 || stats.Completed != 0 {
		t.Fatalf("mid-duplex stats = %#v", stats)
	}
	if _, err := writer.Write([]byte("payload")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if string(body) != "done" {
		t.Fatalf("body = %q", body)
	}
	waitFor(t, 5*time.Second, func() bool { return transport.Stats().Active == 0 })
}

func TestFullCloseClosesBlockedUploadSource(t *testing.T) {
	t.Parallel()

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.WriteHeader(http.StatusOK)
		response.(http.Flusher).Flush()
		<-request.Context().Done()
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()

	transport, err := NewTransport(Config{
		Protocols: []Protocol{ProtocolHTTP2},
		TLSConfig: verifiedTestTLSConfig(t, server),
	})
	if err != nil {
		t.Fatal(err)
	}
	upload := newBlockingUploadBody()
	request, err := http.NewRequest(http.MethodPost, server.URL, upload)
	if err != nil {
		t.Fatal(err)
	}
	responseResult := make(chan *http.Response, 1)
	errorResult := make(chan error, 1)
	go func() {
		response, requestErr := (&http.Client{Transport: transport}).Do(request)
		responseResult <- response
		errorResult <- requestErr
	}()
	select {
	case <-upload.started:
	case <-time.After(5 * time.Second):
		t.Fatal("upload read did not start")
	}
	var response *http.Response
	select {
	case response = <-responseResult:
		if err := <-errorResult; err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("response headers were not returned")
	}
	if err := transport.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-upload.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("blocked upload source was not closed")
	}
	if _, err := response.Body.Read(make([]byte, 1)); !errors.Is(err, ErrTransportClosed) {
		t.Fatalf("response read error = %v, want ErrTransportClosed", err)
	}
	_ = response.Body.Close()
	waitFor(t, 5*time.Second, func() bool { return transport.Stats().Active == 0 })
}

func TestPooledDialWaiterCancellationIsolation(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(response, "ok")
	}))
	defer server.Close()

	dialStarted := make(chan struct{})
	releaseDial := make(chan struct{})
	var startOnce sync.Once
	var dials atomic.Int64
	transport, err := NewTransport(Config{
		Protocols: []Protocol{ProtocolHTTP1},
		Pool:      PoolConfig{MaxConnectionsPerHost: 1},
		DialContext: func(ctx context.Context, request DialRequest) (DialResult, error) {
			dials.Add(1)
			startOnce.Do(func() { close(dialStarted) })
			select {
			case <-releaseDial:
			case <-ctx.Done():
				return DialResult{}, context.Cause(ctx)
			}
			conn, err := (&net.Dialer{}).DialContext(ctx, request.Network, request.Address)
			return DialResult{Conn: conn}, err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	client := &http.Client{Transport: transport}

	firstContext, cancelFirst := context.WithCancel(context.Background())
	firstRequest, err := http.NewRequestWithContext(firstContext, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	firstResult := make(chan error, 1)
	go func() {
		response, requestErr := client.Do(firstRequest)
		if response != nil {
			response.Body.Close()
		}
		firstResult <- requestErr
	}()
	<-dialStarted

	secondResult := make(chan error, 1)
	go func() {
		response, requestErr := client.Get(server.URL)
		if response != nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
		secondResult <- requestErr
	}()
	waitFor(t, 5*time.Second, func() bool { return transport.Stats().RoundTrips == 2 })
	cancelFirst()
	select {
	case err := <-firstResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("first error = %v, want context cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first waiter did not cancel")
	}
	close(releaseDial)
	select {
	case err := <-secondResult:
		if err != nil {
			t.Fatalf("second waiter failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second waiter did not complete")
	}
	if got := dials.Load(); got != 1 {
		t.Fatalf("dial count = %d, want 1", got)
	}
}

func TestFullCloseCancelsUnconsumedDial(t *testing.T) {
	t.Parallel()

	dialStarted := make(chan struct{})
	dialStopped := make(chan struct{})
	transport, err := NewTransport(Config{
		Protocols: []Protocol{ProtocolHTTP1},
		DialContext: func(ctx context.Context, _ DialRequest) (DialResult, error) {
			close(dialStarted)
			<-ctx.Done()
			close(dialStopped)
			return DialResult{}, context.Cause(ctx)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, requestErr := (&http.Client{Transport: transport}).Get("http://example.com/")
		result <- requestErr
	}()
	select {
	case <-dialStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("dial did not start")
	}
	if err := transport.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrTransportClosed) {
			t.Fatalf("request error = %v, want ErrTransportClosed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("request did not stop")
	}
	select {
	case <-dialStopped:
	case <-time.After(5 * time.Second):
		t.Fatal("dial was left running after full close")
	}
	if stats := transport.Stats(); stats.Active != 0 || !stats.Closed {
		t.Fatalf("stats = %#v", stats)
	}
}

func TestCloseIdleDoesNotInterruptActiveBodyAndFullCloseDoes(t *testing.T) {
	t.Parallel()

	headersSent := make(chan struct{})
	releaseBody := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.WriteHeader(http.StatusOK)
		response.(http.Flusher).Flush()
		close(headersSent)
		select {
		case <-releaseBody:
			_, _ = io.WriteString(response, "body")
		case <-request.Context().Done():
		}
	}))
	defer server.Close()

	transport, err := NewTransport(Config{Protocols: []Protocol{ProtocolHTTP1}})
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: transport}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	<-headersSent
	transport.CloseIdleConnections()
	close(releaseBody)
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if string(body) != "body" {
		t.Fatalf("body = %q", body)
	}

	blockingStarted := make(chan struct{})
	blockingServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.WriteHeader(http.StatusOK)
		response.(http.Flusher).Flush()
		close(blockingStarted)
		<-request.Context().Done()
	}))
	defer blockingServer.Close()
	blockingResponse, err := client.Get(blockingServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	<-blockingStarted
	if err := transport.Close(); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 1)
	if _, err := blockingResponse.Body.Read(buffer); !errors.Is(err, ErrTransportClosed) {
		t.Fatalf("body read error = %v, want ErrTransportClosed", err)
	}
	_ = blockingResponse.Body.Close()
	if _, err := client.Get(server.URL); !errors.Is(err, ErrTransportClosed) {
		t.Fatalf("post-close error = %v, want ErrTransportClosed", err)
	}
	stats := transport.Stats()
	if !stats.Closed || stats.Active != 0 {
		t.Fatalf("stats after close = %#v", stats)
	}
}

func TestInvalidCustomDialResultClosesConnection(t *testing.T) {
	t.Parallel()

	clientSide, peerSide := net.Pipe()
	defer peerSide.Close()
	transport, err := NewTransport(Config{
		Protocols: []Protocol{ProtocolHTTP2},
		DialContext: func(context.Context, DialRequest) (DialResult, error) {
			return DialResult{Conn: clientSide}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	_, err = (&http.Client{Transport: transport}).Get("https://example.com/")
	if !errors.Is(err, ErrInvalidDialResult) {
		t.Fatalf("error = %v, want ErrInvalidDialResult", err)
	}
	_ = peerSide.SetReadDeadline(time.Now().Add(time.Second))
	buffer := make([]byte, 1)
	if _, err := peerSide.Read(buffer); !errors.Is(err, io.EOF) {
		t.Fatalf("peer read error = %v, want EOF", err)
	}
}

func verifiedTestTLSConfig(t *testing.T, server *httptest.Server) *tls.Config {
	t.Helper()
	roots, serverName := testServerTrust(t, server)
	return &tls.Config{RootCAs: roots, ServerName: serverName}
}

func testServerTrust(t *testing.T, server *httptest.Server) (*x509.CertPool, string) {
	t.Helper()
	certificate := server.Certificate()
	roots := x509.NewCertPool()
	roots.AddCert(certificate)
	if len(certificate.DNSNames) != 0 {
		return roots, certificate.DNSNames[0]
	}
	if len(certificate.IPAddresses) != 0 {
		return roots, certificate.IPAddresses[0].String()
	}
	t.Fatal("test server certificate has no usable identity")
	return nil, ""
}

func normalizeUTLSState(state utls.ConnectionState) tls.ConnectionState {
	return tls.ConnectionState{
		Version:                     state.Version,
		HandshakeComplete:           state.HandshakeComplete,
		DidResume:                   state.DidResume,
		CipherSuite:                 state.CipherSuite,
		NegotiatedProtocol:          state.NegotiatedProtocol,
		NegotiatedProtocolIsMutual:  state.NegotiatedProtocolIsMutual,
		ServerName:                  state.ServerName,
		PeerCertificates:            state.PeerCertificates,
		VerifiedChains:              state.VerifiedChains,
		SignedCertificateTimestamps: state.SignedCertificateTimestamps,
		OCSPResponse:                state.OCSPResponse,
		TLSUnique:                   state.TLSUnique,
		ECHAccepted:                 state.ECHAccepted,
	}
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition did not become true before timeout")
		}
		time.Sleep(time.Millisecond)
	}
}

type blockingUploadBody struct {
	started chan struct{}
	closed  chan struct{}
	start   sync.Once
	close   sync.Once
}

func newBlockingUploadBody() *blockingUploadBody {
	return &blockingUploadBody{started: make(chan struct{}), closed: make(chan struct{})}
}

func (body *blockingUploadBody) Read([]byte) (int, error) {
	body.start.Do(func() { close(body.started) })
	<-body.closed
	return 0, io.EOF
}

func (body *blockingUploadBody) Close() error {
	body.close.Do(func() { close(body.closed) })
	return nil
}
