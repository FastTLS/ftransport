package ftransport

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"slices"
	"sync"
	"time"
)

type orderedH1Transport struct {
	base        *http.Transport
	staticOrder []string
}

func (transport *orderedH1Transport) RoundTrip(request *http.Request) (*http.Response, error) {
	order := transport.staticOrder
	if options, ok := requestOptionsValueFromRequest(request); ok && options.HeaderOrder != nil {
		order = options.HeaderOrder
	}

	var selectedMu sync.Mutex
	var selected *orderedH1Conn
	trace := &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			conn, _ := info.Conn.(*orderedH1Conn)
			if conn != nil {
				conn.prepare(order)
			}
			selectedMu.Lock()
			selected = conn
			selectedMu.Unlock()
		},
		WroteRequest: func(httptrace.WroteRequestInfo) {
			selectedMu.Lock()
			conn := selected
			selected = nil
			selectedMu.Unlock()
			if conn != nil {
				conn.finishRequest()
			}
		},
	}
	clone := request.Clone(httptrace.WithClientTrace(request.Context(), trace))
	return transport.base.RoundTrip(clone)
}

func (transport *orderedH1Transport) CloseIdleConnections() {
	transport.base.CloseIdleConnections()
}

func (t *Transport) newHTTP1Transport(custom bool) idleRoundTripper {
	base := t.newStandardTransport([]Protocol{ProtocolHTTP1}, nil)
	ordered := &orderedH1Transport{base: base, staticOrder: slices.Clone(t.config.HTTP1.HeaderOrder)}
	wrap := func(conn net.Conn) net.Conn {
		return &orderedH1Conn{Conn: conn, awaitingHeader: true, order: slices.Clone(ordered.staticOrder)}
	}
	if custom {
		base.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			conn, err := t.customDial(ctx, network, address, false, ProtocolHTTP1)
			if err != nil {
				return nil, err
			}
			return wrap(conn), nil
		}
		base.DialTLSContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			conn, err := t.customDial(ctx, network, address, true, ProtocolHTTP1)
			if err != nil {
				return nil, err
			}
			return wrap(conn), nil
		}
		return ordered
	}

	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	base.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		t.stats.dials.Add(1)
		conn, err := dialer.DialContext(ctx, network, address)
		if err != nil {
			t.stats.dialErrors.Add(1)
			return nil, err
		}
		return wrap(conn), nil
	}
	base.DialTLSContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		t.stats.dials.Add(1)
		conn, err := dialer.DialContext(ctx, network, address)
		if err != nil {
			t.stats.dialErrors.Add(1)
			return nil, err
		}
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			_ = conn.Close()
			t.stats.dialErrors.Add(1)
			return nil, err
		}
		config := t.config.TLSConfig
		if config == nil {
			config = new(tls.Config)
		} else {
			config = config.Clone()
		}
		if config.ServerName == "" {
			config.ServerName = host
		}
		config.NextProtos = []string{"http/1.1"}
		tlsConn := tls.Client(conn, config)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			t.stats.dialErrors.Add(1)
			return nil, err
		}
		if negotiated := tlsConn.ConnectionState().NegotiatedProtocol; negotiated != "" && negotiated != "http/1.1" {
			_ = tlsConn.Close()
			t.stats.dialErrors.Add(1)
			return nil, errors.New("ftransport: TLS peer did not negotiate HTTP/1.1")
		}
		return wrap(tlsConn), nil
	}
	return ordered
}

type orderedH1Conn struct {
	net.Conn

	mu             sync.Mutex
	awaitingHeader bool
	headerBuffer   []byte
	order          []string
}

func (conn *orderedH1Conn) prepare(order []string) {
	conn.mu.Lock()
	conn.awaitingHeader = true
	conn.headerBuffer = conn.headerBuffer[:0]
	conn.order = order
	conn.mu.Unlock()
}

func (conn *orderedH1Conn) finishRequest() {
	conn.mu.Lock()
	conn.awaitingHeader = true
	conn.headerBuffer = conn.headerBuffer[:0]
	conn.mu.Unlock()
}

func (conn *orderedH1Conn) Write(input []byte) (int, error) {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	if !conn.awaitingHeader {
		return conn.Conn.Write(input)
	}

	conn.headerBuffer = append(conn.headerBuffer, input...)
	end := bytes.Index(conn.headerBuffer, []byte("\r\n\r\n"))
	if end < 0 {
		return len(input), nil
	}
	end += 4
	rewritten, err := reorderH1HeaderBlock(conn.headerBuffer[:end], conn.order)
	if err != nil {
		return 0, err
	}
	output := append(rewritten, conn.headerBuffer[end:]...)
	conn.headerBuffer = nil
	conn.awaitingHeader = false
	if err := writeFull(conn.Conn, output); err != nil {
		return 0, err
	}
	return len(input), nil
}

func (conn *orderedH1Conn) ConnectionState() tls.ConnectionState {
	if provider, ok := conn.Conn.(interface{ ConnectionState() tls.ConnectionState }); ok {
		return provider.ConnectionState()
	}
	return tls.ConnectionState{}
}

type h1HeaderLine struct {
	raw          []byte
	name         []byte
	selectedName string
}

func reorderH1HeaderBlock(block []byte, order []string) ([]byte, error) {
	lines := bytes.Split(block, []byte("\r\n"))
	if len(lines) < 3 || len(lines[0]) == 0 {
		return nil, errors.New("ftransport: malformed HTTP/1.1 request header")
	}
	headerLines := make([]h1HeaderLine, 0, len(lines)-3)
	for _, line := range lines[1:] {
		if len(line) == 0 {
			break
		}
		colon := bytes.IndexByte(line, ':')
		if colon <= 0 {
			return nil, errors.New("ftransport: malformed HTTP/1.1 header field")
		}
		headerLines = append(headerLines, h1HeaderLine{
			raw:  line,
			name: line[:colon],
		})
	}

	used := make([]bool, len(headerLines))
	ordered := make([]h1HeaderLine, 0, len(headerLines))
	for _, requested := range order {
		for index, line := range headerLines {
			if used[index] || !equalFoldASCII(line.name, requested) {
				continue
			}
			line.selectedName = requested
			ordered = append(ordered, line)
			used[index] = true
		}
	}
	for index, line := range headerLines {
		if !used[index] {
			ordered = append(ordered, line)
		}
	}

	var output bytes.Buffer
	output.Grow(len(block))
	output.Write(lines[0])
	output.WriteString("\r\n")
	for _, line := range ordered {
		if line.selectedName == "" {
			output.Write(line.raw)
		} else {
			output.WriteString(line.selectedName)
			output.Write(line.raw[len(line.name):])
		}
		output.WriteString("\r\n")
	}
	output.WriteString("\r\n")
	return output.Bytes(), nil
}

func equalFoldASCII(value []byte, target string) bool {
	if len(value) != len(target) {
		return false
	}
	for index, left := range value {
		right := target[index]
		if left >= 'A' && left <= 'Z' {
			left += 'a' - 'A'
		}
		if right >= 'A' && right <= 'Z' {
			right += 'a' - 'A'
		}
		if left != right {
			return false
		}
	}
	return true
}

func writeFull(writer io.Writer, data []byte) error {
	for len(data) != 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

var _ idleRoundTripper = (*orderedH1Transport)(nil)
