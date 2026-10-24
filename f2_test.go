package ftransport

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FastTLS/ftransport/internal/xnet/http2"
	"github.com/FastTLS/ftransport/internal/xnet/http2/hpack"
)

func TestHTTP1HeaderOrderOnReusedConnection(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	captured := make(chan string, 2)
	serverError := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverError <- acceptErr
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		for range 2 {
			var header bytes.Buffer
			for {
				line, readErr := reader.ReadString('\n')
				if readErr != nil {
					serverError <- readErr
					return
				}
				header.WriteString(line)
				if line == "\r\n" {
					break
				}
			}
			captured <- header.String()
			if _, writeErr := io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"); writeErr != nil {
				serverError <- writeErr
				return
			}
		}
		serverError <- nil
	}()

	transport, err := NewTransport(Config{
		Protocols: []Protocol{ProtocolHTTP1},
		HTTP1: HTTP1Config{HeaderOrder: []string{
			"X-Third", "user-agent", "Host", "X-First",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	client := &http.Client{Transport: transport}

	orders := [][]string{
		{"X-Third", "user-agent", "Host", "X-First"},
		{"Host", "X-First", "X-Third", "user-agent"},
	}
	for index, order := range orders {
		request, err := http.NewRequest(http.MethodGet, "http://"+listener.Addr().String()+fmt.Sprintf("/%d", index), nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header["X-First"] = []string{"one"}
		request.Header["X-Third"] = []string{"three"}
		request.Header["User-Agent"] = []string{"ftransport-f2"}
		if index == 1 {
			request, err = WithRequestOptions(request, RequestOptions{HeaderOrder: order})
			if err != nil {
				t.Fatal(err)
			}
		}
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
	}

	for _, order := range orders {
		wire := <-captured
		assertHeaderLineOrder(t, wire, order)
	}
	if err := <-serverError; err != nil {
		t.Fatal(err)
	}
}

func assertHeaderLineOrder(t *testing.T, wire string, order []string) {
	t.Helper()
	previous := -1
	for _, name := range order {
		index := strings.Index(wire, "\r\n"+name+":")
		if index < 0 {
			t.Fatalf("header %q not found in wire request:\n%s", name, wire)
		}
		if index <= previous {
			t.Fatalf("header %q is out of order in wire request:\n%s", name, wire)
		}
		previous = index
	}
}

type h2WireCapture struct {
	settings        []HTTP2Setting
	window          uint32
	initialStream   uint32
	initialPriority http2.PriorityParam
	headerPriority  http2.PriorityParam
	headerFields    []hpack.HeaderField
}

func TestHTTP2ConfiguredWireIdentity(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverError := make(chan error, 1)
	capture := make(chan h2WireCapture, 1)
	releaseServer := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseServer) }) }
	defer release()
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverError <- acceptErr
			return
		}
		serveH2WireCapture(conn, capture, serverError, releaseServer)
	}()

	var dials atomic.Int64
	transport, err := NewTransport(Config{
		Protocols: []Protocol{ProtocolHTTP2},
		DialContext: func(ctx context.Context, _ DialRequest) (DialResult, error) {
			if dials.Add(1) != 1 {
				return DialResult{}, errors.New("unexpected second dial")
			}
			conn, dialErr := (&net.Dialer{}).DialContext(ctx, "tcp", listener.Addr().String())
			if dialErr != nil {
				return DialResult{}, dialErr
			}
			state := &tls.ConnectionState{HandshakeComplete: true, NegotiatedProtocol: "h2", NegotiatedProtocolIsMutual: true}
			return DialResult{Conn: conn, NegotiatedProtocol: "h2", TLSState: state}, nil
		},
		HTTP2: HTTP2Config{
			InitialSettings: ExplicitHTTP2Settings(
				HTTP2Setting{ID: SettingHeaderTableSize, Value: 4096},
				HTTP2Setting{ID: SettingEnablePush, Value: 0},
				HTTP2Setting{ID: SettingInitialWindowSize, Value: 65535},
			),
			ConnectionWindowIncrement: 1024,
			PriorityFrames: []HTTP2PriorityFrame{{
				StreamID:           3,
				HTTP2PriorityParam: HTTP2PriorityParam{Dependency: 0, Weight: 1},
			}},
			HeaderPriority:    &HTTP2PriorityParam{Dependency: 0, Weight: 16, Exclusive: true},
			PseudoHeaderOrder: []PseudoHeader{PseudoHeaderPath, PseudoHeaderMethod, PseudoHeaderAuthority, PseudoHeaderScheme},
			HeaderOrder:       []string{"X-Second", "X-Secret", "user-agent", "X-First"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()

	request, err := http.NewRequest(http.MethodGet, "https://example.test/resource?q=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-First", "one")
	request.Header.Set("X-Second", "two")
	request.Header.Set("X-Secret", "secret")
	request.Header.Set("User-Agent", "ftransport-f2")
	request, err = WithRequestOptions(request, RequestOptions{SensitiveHeaders: []string{"X-Secret"}})
	if err != nil {
		t.Fatal(err)
	}
	response, err := (&http.Client{Transport: transport}).Do(request)
	if err != nil {
		select {
		case peerErr := <-serverError:
			t.Fatalf("request error: %v; peer error: %v", err, peerErr)
		default:
			t.Fatal(err)
		}
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	release()

	got := <-capture
	wantSettings := []HTTP2Setting{
		{ID: SettingHeaderTableSize, Value: 4096},
		{ID: SettingEnablePush, Value: 0},
		{ID: SettingInitialWindowSize, Value: 65535},
	}
	if fmt.Sprint(got.settings) != fmt.Sprint(wantSettings) {
		t.Fatalf("settings = %#v, want %#v", got.settings, wantSettings)
	}
	if got.window != 1024 || got.initialStream != 3 || got.initialPriority.Weight != 0 {
		t.Fatalf("initial frames = %#v", got)
	}
	if got.headerPriority.StreamDep != 0 || got.headerPriority.Weight != 15 || !got.headerPriority.Exclusive {
		t.Fatalf("header priority = %#v", got.headerPriority)
	}
	wantNames := []string{":path", ":method", ":authority", ":scheme", "x-second", "x-secret", "user-agent", "x-first"}
	if len(got.headerFields) != len(wantNames) {
		t.Fatalf("header fields = %#v", got.headerFields)
	}
	for index, name := range wantNames {
		if got.headerFields[index].Name != name {
			t.Fatalf("header %d = %q, want %q; fields=%#v", index, got.headerFields[index].Name, name, got.headerFields)
		}
	}
	if !got.headerFields[5].Sensitive {
		t.Fatalf("X-Secret was not encoded as never-indexed: %#v", got.headerFields[5])
	}
	if err := <-serverError; err != nil {
		t.Fatal(err)
	}
}

func TestHTTP2ExplicitEmptyAndZeroSettingsOnWire(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		settings HTTP2Settings
		want     []HTTP2Setting
	}{
		{name: "empty", settings: ExplicitHTTP2Settings(), want: []HTTP2Setting{}},
		{
			name:     "initial window zero",
			settings: ExplicitHTTP2Settings(HTTP2Setting{ID: SettingInitialWindowSize, Value: 0}),
			want:     []HTTP2Setting{{ID: SettingInitialWindowSize, Value: 0}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			captured := make(chan []HTTP2Setting, 1)
			go func() {
				conn, acceptErr := listener.Accept()
				if acceptErr != nil {
					captured <- nil
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				reader := bufio.NewReader(conn)
				preface := make([]byte, len(http2.ClientPreface))
				if _, readErr := io.ReadFull(reader, preface); readErr != nil {
					captured <- nil
					return
				}
				frame, readErr := http2.NewFramer(conn, reader).ReadFrame()
				if readErr != nil {
					captured <- nil
					return
				}
				settings, ok := frame.(*http2.SettingsFrame)
				if !ok {
					captured <- nil
					return
				}
				var values []HTTP2Setting
				_ = settings.ForeachSetting(func(setting http2.Setting) error {
					values = append(values, HTTP2Setting{ID: HTTP2SettingID(setting.ID), Value: setting.Val})
					return nil
				})
				if values == nil {
					values = []HTTP2Setting{}
				}
				captured <- values
			}()

			transport, err := NewTransport(Config{
				Protocols: []Protocol{ProtocolHTTP2},
				DialContext: func(ctx context.Context, _ DialRequest) (DialResult, error) {
					conn, dialErr := (&net.Dialer{}).DialContext(ctx, "tcp", listener.Addr().String())
					if dialErr != nil {
						return DialResult{}, dialErr
					}
					state := &tls.ConnectionState{HandshakeComplete: true, NegotiatedProtocol: "h2", NegotiatedProtocolIsMutual: true}
					return DialResult{Conn: conn, NegotiatedProtocol: "h2", TLSState: state}, nil
				},
				HTTP2: HTTP2Config{InitialSettings: test.settings},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer transport.Close()
			request, err := http.NewRequest(http.MethodGet, "https://example.test/", nil)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = transport.RoundTrip(request)
			got := <-captured
			if fmt.Sprint(got) != fmt.Sprint(test.want) {
				t.Fatalf("settings = %#v, want %#v", got, test.want)
			}
		})
	}
}

func serveH2WireCapture(conn net.Conn, captured chan<- h2WireCapture, result chan<- error, release <-chan struct{}) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	reader := bufio.NewReader(conn)
	preface := make([]byte, len(http2.ClientPreface))
	if _, err := io.ReadFull(reader, preface); err != nil {
		result <- err
		return
	}
	if string(preface) != http2.ClientPreface {
		result <- fmt.Errorf("client preface = %q", preface)
		return
	}
	framer := http2.NewFramer(conn, reader)
	var got h2WireCapture
	frame, err := framer.ReadFrame()
	if err != nil {
		result <- err
		return
	}
	settings, ok := frame.(*http2.SettingsFrame)
	if !ok {
		result <- fmt.Errorf("first frame = %T, want SETTINGS", frame)
		return
	}
	if err := settings.ForeachSetting(func(setting http2.Setting) error {
		got.settings = append(got.settings, HTTP2Setting{ID: HTTP2SettingID(setting.ID), Value: setting.Val})
		return nil
	}); err != nil {
		result <- err
		return
	}
	frame, err = framer.ReadFrame()
	if err != nil {
		result <- err
		return
	}
	window, ok := frame.(*http2.WindowUpdateFrame)
	if !ok {
		result <- fmt.Errorf("second frame = %T, want WINDOW_UPDATE", frame)
		return
	}
	got.window = window.Increment
	frame, err = framer.ReadFrame()
	if err != nil {
		result <- err
		return
	}
	priority, ok := frame.(*http2.PriorityFrame)
	if !ok {
		result <- fmt.Errorf("third frame = %T, want PRIORITY", frame)
		return
	}
	got.initialStream = priority.StreamID
	got.initialPriority = priority.PriorityParam
	if err := framer.WriteSettings(); err != nil {
		result <- err
		return
	}

	for {
		frame, err = framer.ReadFrame()
		if err != nil {
			result <- err
			return
		}
		headers, ok := frame.(*http2.HeadersFrame)
		if !ok {
			continue
		}
		got.headerPriority = headers.Priority
		block := slicesForTest(headers.HeaderBlockFragment())
		for !headers.HeadersEnded() {
			continuationFrame, readErr := framer.ReadFrame()
			if readErr != nil {
				result <- readErr
				return
			}
			continuation, ok := continuationFrame.(*http2.ContinuationFrame)
			if !ok {
				result <- fmt.Errorf("header continuation = %T", continuationFrame)
				return
			}
			block = append(block, continuation.HeaderBlockFragment()...)
			if continuation.HeadersEnded() {
				break
			}
		}
		decoder := hpack.NewDecoder(4096, func(field hpack.HeaderField) {
			got.headerFields = append(got.headerFields, field)
		})
		if _, err := decoder.Write(block); err != nil {
			result <- err
			return
		}
		if err := decoder.Close(); err != nil {
			result <- err
			return
		}

		var responseBlock bytes.Buffer
		encoder := hpack.NewEncoder(&responseBlock)
		if err := encoder.WriteField(hpack.HeaderField{Name: ":status", Value: "200"}); err != nil {
			result <- err
			return
		}
		if err := framer.WriteHeaders(http2.HeadersFrameParam{
			StreamID:      headers.StreamID,
			BlockFragment: responseBlock.Bytes(),
			EndHeaders:    true,
			EndStream:     true,
		}); err != nil {
			result <- err
			return
		}
		captured <- got
		result <- nil
		<-release
		return
	}
}

func slicesForTest(value []byte) []byte {
	return append([]byte(nil), value...)
}

func TestHTTP2Streams64MiBWithSlowConsumer(t *testing.T) {
	if testing.Short() {
		t.Skip("64 MiB streaming test")
	}
	const total = 64 << 20
	pattern := bytes.Repeat([]byte("ftransport-f2-stream"), 2048)
	wantHash := sha256.New()
	remaining := total
	for remaining > 0 {
		chunk := pattern
		if len(chunk) > remaining {
			chunk = chunk[:remaining]
		}
		_, _ = wantHash.Write(chunk)
		remaining -= len(chunk)
	}

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		remaining := total
		for remaining > 0 {
			chunk := pattern
			if len(chunk) > remaining {
				chunk = chunk[:remaining]
			}
			if _, err := response.Write(chunk); err != nil {
				return
			}
			remaining -= len(chunk)
		}
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
	response, err := (&http.Client{Transport: transport}).Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	buffer := make([]byte, 32<<10)
	read := 0
	for {
		n, readErr := response.Body.Read(buffer)
		if n != 0 {
			_, _ = hash.Write(buffer[:n])
			read += n
			if read%(1<<20) < n {
				time.Sleep(time.Millisecond)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			t.Fatal(readErr)
		}
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if read != total || !bytes.Equal(hash.Sum(nil), wantHash.Sum(nil)) {
		t.Fatalf("stream result: bytes=%d hash=%x want=%x", read, hash.Sum(nil), wantHash.Sum(nil))
	}
}

func TestHTTP2MaxConnectionsPerHost(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	started := make(chan struct{}, 3)
	release := make(chan struct{}, 3)
	releaseAll := func() {
		for range 3 {
			select {
			case release <- struct{}{}:
			default:
			}
		}
	}
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go serveLimitedH2Peer(conn, started, release)
		}
	}()

	transport, err := NewTransport(Config{
		Protocols: []Protocol{ProtocolHTTP2},
		DialContext: func(ctx context.Context, _ DialRequest) (DialResult, error) {
			conn, dialErr := (&net.Dialer{}).DialContext(ctx, "tcp", listener.Addr().String())
			if dialErr != nil {
				return DialResult{}, dialErr
			}
			state := &tls.ConnectionState{HandshakeComplete: true, NegotiatedProtocol: "h2", NegotiatedProtocolIsMutual: true}
			return DialResult{Conn: conn, NegotiatedProtocol: "h2", TLSState: state}, nil
		},
		HTTP2: HTTP2Config{InitialSettings: ExplicitHTTP2Settings(
			HTTP2Setting{ID: SettingEnablePush, Value: 0},
			HTTP2Setting{ID: SettingInitialWindowSize, Value: 1 << 20},
		)},
		Pool: PoolConfig{MaxConnectionsPerHost: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	client := &http.Client{Transport: transport}
	results := make(chan error, 3)
	startRequest := func() {
		go func() {
			response, requestErr := client.Get("https://example.test/")
			if response != nil {
				_, _ = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
			}
			results <- requestErr
		}()
	}

	// Warm the first connection so its advertised stream limit is known before
	// concurrent requests begin.
	startRequest()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		releaseAll()
		t.Fatal("warm-up request did not start")
	}
	release <- struct{}{}
	if err := <-results; err != nil {
		releaseAll()
		t.Fatal(err)
	}

	startRequest()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		releaseAll()
		t.Fatal("first capped request did not start")
	}
	startRequest()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		releaseAll()
		t.Fatal("second connection slot was not opened")
	}
	startRequest()
	select {
	case <-started:
		releaseAll()
		t.Fatal("third request bypassed the per-host connection cap")
	case <-time.After(100 * time.Millisecond):
	}
	if got := transport.Stats().Dials; got != 2 {
		releaseAll()
		t.Fatalf("dial count before release = %d, want 2", got)
	}
	release <- struct{}{}
	release <- struct{}{}
	for range 2 {
		if err := <-results; err != nil {
			releaseAll()
			t.Fatal(err)
		}
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		releaseAll()
		t.Fatal("waiting request did not use released stream capacity")
	}
	release <- struct{}{}
	if err := <-results; err != nil {
		t.Fatal(err)
	}
	if got := transport.Stats().Dials; got != 2 {
		t.Fatalf("dial count = %d, want 2", got)
	}
}

func serveLimitedH2Peer(conn net.Conn, started chan<- struct{}, release <-chan struct{}) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	preface := make([]byte, len(http2.ClientPreface))
	if _, err := io.ReadFull(reader, preface); err != nil {
		return
	}
	framer := http2.NewFramer(conn, reader)
	if _, err := framer.ReadFrame(); err != nil {
		return
	}
	if err := framer.WriteSettings(http2.Setting{ID: http2.SettingMaxConcurrentStreams, Val: 1}); err != nil {
		return
	}
	for {
		frame, err := framer.ReadFrame()
		if err != nil {
			return
		}
		headers, ok := frame.(*http2.HeadersFrame)
		if !ok {
			continue
		}
		for !headers.HeadersEnded() {
			continuation, readErr := framer.ReadFrame()
			if readErr != nil {
				return
			}
			if continued, ok := continuation.(*http2.ContinuationFrame); ok && continued.HeadersEnded() {
				break
			}
		}
		started <- struct{}{}
		<-release
		var block bytes.Buffer
		encoder := hpack.NewEncoder(&block)
		if err := encoder.WriteField(hpack.HeaderField{Name: ":status", Value: "200"}); err != nil {
			return
		}
		if err := encoder.WriteField(hpack.HeaderField{Name: "content-length", Value: "2"}); err != nil {
			return
		}
		if err := framer.WriteHeaders(http2.HeadersFrameParam{
			StreamID:      headers.StreamID,
			BlockFragment: block.Bytes(),
			EndHeaders:    true,
		}); err != nil {
			return
		}
		if err := framer.WriteData(headers.StreamID, true, []byte("ok")); err != nil {
			return
		}
	}
}

func TestHTTP2ConnectionCapWaiterCancellation(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	started := make(chan struct{}, 2)
	release := make(chan struct{}, 2)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			serveLimitedH2Peer(conn, started, release)
		}
	}()

	transport, err := NewTransport(Config{
		Protocols: []Protocol{ProtocolHTTP2},
		DialContext: func(ctx context.Context, _ DialRequest) (DialResult, error) {
			conn, dialErr := (&net.Dialer{}).DialContext(ctx, "tcp", listener.Addr().String())
			if dialErr != nil {
				return DialResult{}, dialErr
			}
			state := &tls.ConnectionState{HandshakeComplete: true, NegotiatedProtocol: "h2", NegotiatedProtocolIsMutual: true}
			return DialResult{Conn: conn, NegotiatedProtocol: "h2", TLSState: state}, nil
		},
		Pool: PoolConfig{MaxConnectionsPerHost: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	client := &http.Client{Transport: transport}

	warmResult := make(chan error, 1)
	go func() {
		response, requestErr := client.Get("https://example.test/warm")
		if response != nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
		warmResult <- requestErr
	}()
	<-started
	release <- struct{}{}
	if err := <-warmResult; err != nil {
		t.Fatal(err)
	}

	activeResult := make(chan error, 1)
	go func() {
		response, requestErr := client.Get("https://example.test/active")
		if response != nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
		activeResult <- requestErr
	}()
	<-started

	waiterContext, cancelWaiter := context.WithCancel(context.Background())
	waiter, err := http.NewRequestWithContext(waiterContext, http.MethodGet, "https://example.test/waiter", nil)
	if err != nil {
		t.Fatal(err)
	}
	waiterResult := make(chan error, 1)
	go func() {
		_, requestErr := client.Do(waiter)
		waiterResult <- requestErr
	}()
	waitFor(t, 5*time.Second, func() bool { return transport.Stats().RoundTrips == 3 })
	cancelWaiter()
	select {
	case err := <-waiterResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waiter error = %v, want context cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("connection-cap waiter did not cancel")
	}
	if got := transport.Stats().Dials; got != 1 {
		t.Fatalf("dial count = %d, want 1", got)
	}
	release <- struct{}{}
	if err := <-activeResult; err != nil {
		t.Fatal(err)
	}
}
