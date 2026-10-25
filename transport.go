package ftransport

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/FastTLS/ftransport/internal/xnet/http2"
)

// Transport is an HTTP/1.1 and HTTP/2 client transport. It is safe for
// concurrent use. Construct it with NewTransport and close it when its owning
// session ends. A Transport must not be copied after first use.
type Transport struct {
	config   Config
	protocol Protocol
	base     idleRoundTripper
	wireOpts bool

	mu     sync.Mutex
	closed bool
	nextID uint64
	active map[uint64]activeOperation

	stats transportCounters
}

type idleRoundTripper interface {
	http.RoundTripper
	CloseIdleConnections()
}

type activeOperation struct {
	cancel context.CancelCauseFunc
	body   *trackedRequestBody
}

type transportCounters struct {
	roundTrips atomic.Uint64
	responses  atomic.Uint64
	failed     atomic.Uint64
	completed  atomic.Uint64
	active     atomic.Int64
	dials      atomic.Uint64
	dialErrors atomic.Uint64
	closed     atomic.Bool
}

// Stats is a point-in-time, transport-scoped snapshot. Counters contain no
// request URLs, headers, credentials, or body data.
type Stats struct {
	// RoundTrips is the number of requests admitted before full close.
	RoundTrips uint64
	// Responses is the number of requests that returned response headers.
	Responses uint64
	// Failed is the number of admitted requests that failed before returning a
	// usable response.
	Failed uint64
	// Completed counts operations whose upload source and response side have
	// both reached EOF, closed, or failed.
	Completed uint64
	// Active counts admitted requests that have not reached a terminal state.
	Active uint64
	// Dials is the number of standard or custom connection attempts.
	Dials uint64
	// DialErrors includes network errors and rejected custom dial results.
	DialErrors uint64
	// Closed reports whether full Close has begun.
	Closed bool
}

// NewTransport validates and deep-copies config before any network activity.
// Neutral H1/H2 and F2 deterministic H1/H2 wire configuration are supported.
// Wire configuration requires one explicit protocol so it cannot be silently
// bypassed by ALPN negotiation.
func NewTransport(config Config) (*Transport, error) {
	config = config.Clone()
	if err := config.Validate(); err != nil {
		return nil, &ConfigError{Err: err}
	}
	protocols := resolvedProtocols(config.Protocols)
	if err := validateWireProtocolSelection(config, protocols); err != nil {
		return nil, &ConfigError{Field: "Protocols", Err: err}
	}
	if config.DialContext != nil && len(protocols) != 1 {
		return nil, &ConfigError{
			Field: "Protocols",
			Err:   errors.New("custom dialing requires exactly one explicit protocol"),
		}
	}
	if config.DialContext != nil && config.TLSConfig != nil {
		return nil, &ConfigError{
			Field: "TLSConfig",
			Err:   errors.New("custom dialers own TLS configuration and verification"),
		}
	}

	t := &Transport{
		config: config,
		active: make(map[uint64]activeOperation),
	}
	if len(protocols) == 1 {
		t.protocol = protocols[0]
	}

	if len(protocols) == 1 && protocols[0] == ProtocolHTTP2 {
		hasPoolCountLimits := config.Pool.MaxConnectionsPerHost != 0 || config.Pool.MaxIdleConnections != 0 || config.Pool.MaxIdlePerHost != 0
		if config.DialContext == nil && !hasHTTP2WireConfig(config.HTTP2) && hasPoolCountLimits {
			t.base = t.newStandardTransport(protocols, nil)
			return t, nil
		}
		if config.Pool.MaxIdleConnections != 0 || config.Pool.MaxIdlePerHost != 0 {
			return nil, &UnsupportedFeatureError{Feature: "custom HTTP/2 idle-connection count limits"}
		}
		t.base = t.newHTTP2Transport(config.DialContext != nil)
		t.wireOpts = true
		return t, nil
	}
	if len(protocols) == 1 && protocols[0] == ProtocolHTTP1 {
		t.base = t.newHTTP1Transport(config.DialContext != nil)
		t.wireOpts = true
		return t, nil
	}
	if config.DialContext == nil {
		t.base = t.newStandardTransport(protocols, nil)
		return t, nil
	}
	return nil, &ConfigError{Err: errors.New("unreachable protocol selection")}
}

func validateWireProtocolSelection(config Config, protocols []Protocol) error {
	hasH1 := len(config.HTTP1.HeaderOrder) != 0
	hasH2 := hasHTTP2WireConfig(config.HTTP2)
	if (hasH1 || hasH2) && len(protocols) != 1 {
		return errors.New("wire configuration requires exactly one explicit protocol")
	}
	if hasH1 && protocols[0] != ProtocolHTTP1 {
		return errors.New("HTTP1 configuration requires ProtocolHTTP1")
	}
	if hasH2 && protocols[0] != ProtocolHTTP2 {
		return errors.New("HTTP2 configuration requires ProtocolHTTP2")
	}
	return nil
}

func hasHTTP2WireConfig(config HTTP2Config) bool {
	return config.InitialSettings.Present ||
		config.ConnectionWindowIncrement != 0 ||
		len(config.PriorityFrames) != 0 ||
		config.HeaderPriority != nil ||
		len(config.PseudoHeaderOrder) != 0 ||
		len(config.HeaderOrder) != 0
}

func resolvedProtocols(protocols []Protocol) []Protocol {
	if len(protocols) == 0 {
		return []Protocol{ProtocolHTTP1, ProtocolHTTP2}
	}
	return slices.Clone(protocols)
}

type customDialFunc func(ctx context.Context, network, address string, tlsConnection bool, expected Protocol) (net.Conn, error)

func (t *Transport) newStandardTransport(protocols []Protocol, custom customDialFunc) *http.Transport {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = nil
	base.DisableCompression = true
	base.TLSClientConfig = t.config.TLSConfig

	configured := new(http.Protocols)
	for _, protocol := range protocols {
		switch protocol {
		case ProtocolHTTP1:
			configured.SetHTTP1(true)
		case ProtocolHTTP2:
			configured.SetHTTP2(true)
		}
	}
	base.Protocols = configured
	base.ForceAttemptHTTP2 = configured.HTTP2()

	if t.config.Pool.MaxConnectionsPerHost != 0 {
		base.MaxConnsPerHost = t.config.Pool.MaxConnectionsPerHost
	}
	if t.config.Pool.MaxIdleConnections != 0 {
		base.MaxIdleConns = t.config.Pool.MaxIdleConnections
	}
	if t.config.Pool.MaxIdlePerHost != 0 {
		base.MaxIdleConnsPerHost = t.config.Pool.MaxIdlePerHost
	}
	if t.config.Pool.IdleConnectionTimeout != 0 {
		base.IdleConnTimeout = t.config.Pool.IdleConnectionTimeout
	}

	if custom == nil {
		dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
		base.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			t.stats.dials.Add(1)
			conn, err := dialer.DialContext(ctx, network, address)
			if err != nil {
				t.stats.dialErrors.Add(1)
			}
			return conn, err
		}
		return base
	}

	base.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return custom(ctx, network, address, false, ProtocolHTTP1)
	}
	base.DialTLSContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return custom(ctx, network, address, true, ProtocolHTTP1)
	}
	return base
}

func (t *Transport) newHTTP2Transport(custom bool) *http2.Transport {
	base := &http2.Transport{
		DisableCompression:        true,
		IdleConnTimeout:           t.config.Pool.IdleConnectionTimeout,
		ConnectionWindowIncrement: t.config.HTTP2.ConnectionWindowIncrement,
		MaxConnectionsPerHost:     t.config.Pool.MaxConnectionsPerHost,
	}
	if custom {
		base.DialTLSContext = func(ctx context.Context, network, address string, _ *tls.Config) (net.Conn, error) {
			return t.customDial(ctx, network, address, true, ProtocolHTTP2)
		}
	} else {
		base.DialTLSContext = t.standardH2Dial
	}
	if t.config.HTTP2.InitialSettings.Present {
		base.InitialSettings = make([]http2.Setting, len(t.config.HTTP2.InitialSettings.Values))
		for index, setting := range t.config.HTTP2.InitialSettings.Values {
			base.InitialSettings[index] = http2.Setting{ID: http2.SettingID(setting.ID), Val: setting.Value}
		}
	}
	base.InitialPriorityFrames = make([]http2.InitialPriorityFrame, len(t.config.HTTP2.PriorityFrames))
	for index, priority := range t.config.HTTP2.PriorityFrames {
		base.InitialPriorityFrames[index] = http2.InitialPriorityFrame{
			StreamID: priority.StreamID,
			Priority: http2.PriorityParam{
				StreamDep: priority.Dependency,
				Weight:    uint8(priority.Weight - 1),
				Exclusive: priority.Exclusive,
			},
		}
	}
	if priority := t.config.HTTP2.HeaderPriority; priority != nil {
		base.HeaderPriority = &http2.PriorityParam{
			StreamDep: priority.Dependency,
			Weight:    uint8(priority.Weight - 1),
			Exclusive: priority.Exclusive,
		}
	}
	staticPseudoOrder := make([]string, len(t.config.HTTP2.PseudoHeaderOrder))
	for index, name := range t.config.HTTP2.PseudoHeaderOrder {
		staticPseudoOrder[index] = string(name)
	}
	staticHeaderOptions := http2.HeaderOptions{
		PseudoHeaderOrder: staticPseudoOrder,
		HeaderOrder:       t.config.HTTP2.HeaderOrder,
	}
	base.RequestHeaderOptions = func(request *http.Request) http2.HeaderOptions {
		options, _ := requestOptionsValueFromRequest(request)
		pseudoOrder := staticHeaderOptions.PseudoHeaderOrder
		if options.PseudoHeaderOrder != nil {
			pseudoOrder = make([]string, len(options.PseudoHeaderOrder))
			for index, name := range options.PseudoHeaderOrder {
				pseudoOrder[index] = string(name)
			}
		}
		headerOrder := staticHeaderOptions.HeaderOrder
		if options.HeaderOrder != nil {
			headerOrder = options.HeaderOrder
		}
		return http2.HeaderOptions{
			PseudoHeaderOrder: pseudoOrder,
			HeaderOrder:       headerOrder,
			SensitiveHeaders:  options.SensitiveHeaders,
		}
	}
	return base
}

func (t *Transport) standardH2Dial(ctx context.Context, network, address string, _ *tls.Config) (net.Conn, error) {
	t.stats.dials.Add(1)
	conn, err := (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, network, address)
	if err != nil {
		t.stats.dialErrors.Add(1)
		return nil, err
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		_ = conn.Close()
		t.stats.dialErrors.Add(1)
		return nil, &OperationError{Operation: "split dial address", Protocol: ProtocolHTTP2, Err: err}
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
	config.NextProtos = []string{"h2"}
	tlsConn := tls.Client(conn, config)
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		_ = conn.Close()
		t.stats.dialErrors.Add(1)
		return nil, err
	}
	if negotiated := tlsConn.ConnectionState().NegotiatedProtocol; negotiated != "h2" {
		_ = tlsConn.Close()
		t.stats.dialErrors.Add(1)
		return nil, fmt.Errorf("%w: negotiated %q, expected h2", ErrInvalidDialResult, negotiated)
	}
	return tlsConn, nil
}

func (t *Transport) customDial(ctx context.Context, network, address string, tlsConnection bool, expected Protocol) (net.Conn, error) {
	t.stats.dials.Add(1)
	serverName := ""
	if tlsConnection {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			t.stats.dialErrors.Add(1)
			return nil, &OperationError{Operation: "split dial address", Protocol: expected, Err: err}
		}
		serverName = host
	}
	alpn := []string{expected.String()}
	result, err := t.config.DialContext(ctx, DialRequest{
		Network:       network,
		Address:       address,
		TLS:           tlsConnection,
		ServerName:    serverName,
		ALPNProtocols: alpn,
	})
	if err != nil {
		if result.Conn != nil {
			_ = result.Conn.Close()
		}
		t.stats.dialErrors.Add(1)
		return nil, err
	}
	conn, err := validateDialResult(result, tlsConnection, expected)
	if err != nil {
		if result.Conn != nil {
			_ = result.Conn.Close()
		}
		t.stats.dialErrors.Add(1)
		return nil, err
	}
	return conn, nil
}

func validateDialResult(result DialResult, tlsConnection bool, expected Protocol) (net.Conn, error) {
	if result.Conn == nil {
		return nil, fmt.Errorf("%w: nil connection", ErrInvalidDialResult)
	}
	if !tlsConnection {
		if result.TLSState != nil || result.NegotiatedProtocol != "" {
			return nil, fmt.Errorf("%w: clear-text dial returned TLS state", ErrInvalidDialResult)
		}
		return result.Conn, nil
	}
	if result.TLSState == nil {
		return nil, fmt.Errorf("%w: TLS dial returned no normalized state", ErrInvalidDialResult)
	}
	state := cloneTLSState(*result.TLSState)
	if !state.HandshakeComplete {
		return nil, fmt.Errorf("%w: TLS handshake is not complete", ErrInvalidDialResult)
	}
	negotiated := result.NegotiatedProtocol
	if negotiated == "" {
		negotiated = state.NegotiatedProtocol
	}
	if state.NegotiatedProtocol != "" && negotiated != state.NegotiatedProtocol {
		return nil, fmt.Errorf("%w: ALPN result and TLS state disagree", ErrInvalidDialResult)
	}
	if negotiated != "" && !state.NegotiatedProtocolIsMutual {
		return nil, fmt.Errorf("%w: negotiated ALPN is not mutual", ErrInvalidDialResult)
	}
	if expected == ProtocolHTTP2 && negotiated != "h2" {
		return nil, fmt.Errorf("%w: negotiated %q, expected h2", ErrInvalidDialResult, negotiated)
	}
	if expected == ProtocolHTTP1 && negotiated != "" && negotiated != "http/1.1" {
		return nil, fmt.Errorf("%w: negotiated %q, expected http/1.1", ErrInvalidDialResult, negotiated)
	}
	state.NegotiatedProtocol = negotiated
	return &connectionStateConn{Conn: result.Conn, state: state}, nil
}

type connectionStateConn struct {
	net.Conn
	state tls.ConnectionState
}

func (conn *connectionStateConn) ConnectionState() tls.ConnectionState {
	return cloneTLSState(conn.state)
}

func cloneTLSState(state tls.ConnectionState) tls.ConnectionState {
	state.PeerCertificates = slices.Clone(state.PeerCertificates)
	state.VerifiedChains = slices.Clone(state.VerifiedChains)
	for i := range state.VerifiedChains {
		state.VerifiedChains[i] = slices.Clone(state.VerifiedChains[i])
	}
	state.SignedCertificateTimestamps = cloneByteSlices(state.SignedCertificateTimestamps)
	state.OCSPResponse = slices.Clone(state.OCSPResponse)
	state.TLSUnique = slices.Clone(state.TLSUnique)
	return state
}

// RoundTrip implements http.RoundTripper. Request tracking remains active
// until Response.Body reaches EOF or is closed.
func (t *Transport) RoundTrip(request *http.Request) (*http.Response, error) {
	if t == nil || t.base == nil {
		closeRequestBody(request)
		return nil, &OperationError{Operation: "round trip", Err: errors.Join(ErrInvalidConfig, errors.New("transport was not constructed by NewTransport"))}
	}
	if request == nil {
		return nil, &OperationError{Operation: "round trip", Protocol: t.protocol, Err: errors.New("nil request")}
	}
	if request.URL == nil {
		closeRequestBody(request)
		return nil, &OperationError{Operation: "round trip", Protocol: t.protocol, Err: errors.New("nil request URL")}
	}
	if options, ok := requestOptionsValueFromRequest(request); ok && requestOptionsPresent(options) {
		if !t.wireOpts {
			closeRequestBody(request)
			return nil, &UnsupportedFeatureError{Feature: "per-request wire options require exactly one explicit protocol"}
		}
		if t.protocol == ProtocolHTTP1 && (options.PseudoHeaderOrder != nil || options.SensitiveHeaders != nil) {
			closeRequestBody(request)
			return nil, &UnsupportedFeatureError{Feature: "HTTP/2 pseudo-header or HPACK sensitivity options on HTTP/1.1"}
		}
	}

	ctx, cancel := context.WithCancelCause(request.Context())
	requestHasBody := request.Body != nil && request.Body != http.NoBody
	lifecycle := newRequestLifecycle(!requestHasBody, nil)
	var upload *trackedRequestBody
	if requestHasBody {
		upload = &trackedRequestBody{body: request.Body, lifecycle: lifecycle}
	}
	finish, err := t.begin(cancel, upload)
	if err != nil {
		cancel(nil)
		if upload != nil {
			_ = upload.Close()
		}
		return nil, err
	}
	t.stats.roundTrips.Add(1)
	lifecycle.finish = finish

	var stateMu sync.Mutex
	var observedState *tls.ConnectionState
	trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
		if conn, ok := info.Conn.(interface{ ConnectionState() tls.ConnectionState }); ok {
			state := cloneTLSState(conn.ConnectionState())
			stateMu.Lock()
			observedState = &state
			stateMu.Unlock()
		}
	}}
	ctx = httptrace.WithClientTrace(ctx, trace)
	clone := request.Clone(ctx)
	if upload != nil {
		clone.Body = upload
	}
	response, roundTripErr := t.base.RoundTrip(clone)
	if roundTripErr != nil {
		lifecycle.responseFinished()
		t.stats.failed.Add(1)
		return nil, &OperationError{Operation: "round trip", Protocol: t.protocol, Err: classifyClose(ctx, roundTripErr)}
	}
	if cause := context.Cause(ctx); errors.Is(cause, ErrTransportClosed) {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		lifecycle.responseFinished()
		t.stats.failed.Add(1)
		return nil, &OperationError{Operation: "round trip", Protocol: t.protocol, Err: ErrTransportClosed}
	}
	if response == nil || response.Body == nil {
		lifecycle.responseFinished()
		t.stats.failed.Add(1)
		return nil, &OperationError{Operation: "round trip", Protocol: t.protocol, Err: errors.New("underlying transport returned a nil response or body")}
	}
	if response.TLS == nil {
		stateMu.Lock()
		if observedState != nil {
			state := cloneTLSState(*observedState)
			response.TLS = &state
		}
		stateMu.Unlock()
	}
	response.Request = request
	response.Body = &trackedResponseBody{body: response.Body, ctx: ctx, lifecycle: lifecycle}
	t.stats.responses.Add(1)
	return response, nil
}

func closeRequestBody(request *http.Request) {
	if request != nil && request.Body != nil && request.Body != http.NoBody {
		_ = request.Body.Close()
	}
}

func requestOptionsPresent(options RequestOptions) bool {
	return options.HeaderOrder != nil || options.PseudoHeaderOrder != nil || options.SensitiveHeaders != nil
}

func (t *Transport) begin(cancel context.CancelCauseFunc, body *trackedRequestBody) (func(), error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, ErrTransportClosed
	}
	t.nextID++
	id := t.nextID
	t.active[id] = activeOperation{cancel: cancel, body: body}
	t.stats.active.Add(1)
	t.mu.Unlock()

	var once sync.Once
	done := func() {
		once.Do(func() {
			t.mu.Lock()
			delete(t.active, id)
			closed := t.closed
			t.mu.Unlock()
			t.stats.active.Add(-1)
			t.stats.completed.Add(1)
			if closed {
				t.base.CloseIdleConnections()
			}
		})
	}
	return done, nil
}

func classifyClose(ctx context.Context, err error) error {
	if errors.Is(context.Cause(ctx), ErrTransportClosed) {
		return errors.Join(ErrTransportClosed, err)
	}
	return err
}

type requestLifecycle struct {
	mu           sync.Mutex
	requestDone  bool
	responseDone bool
	finish       func()
}

func newRequestLifecycle(requestDone bool, finish func()) *requestLifecycle {
	return &requestLifecycle{requestDone: requestDone, finish: finish}
}

func (lifecycle *requestLifecycle) requestFinished() {
	lifecycle.markFinished(true)
}

func (lifecycle *requestLifecycle) responseFinished() {
	lifecycle.markFinished(false)
}

func (lifecycle *requestLifecycle) markFinished(request bool) {
	lifecycle.mu.Lock()
	if request {
		lifecycle.requestDone = true
	} else {
		lifecycle.responseDone = true
	}
	finished := lifecycle.requestDone && lifecycle.responseDone
	lifecycle.mu.Unlock()
	if finished {
		lifecycle.finish()
	}
}

type trackedRequestBody struct {
	body      io.ReadCloser
	lifecycle *requestLifecycle
	closeOnce sync.Once
	closeErr  error
}

func (body *trackedRequestBody) Read(buffer []byte) (int, error) {
	n, err := body.body.Read(buffer)
	if err != nil {
		body.lifecycle.requestFinished()
	}
	return n, err
}

func (body *trackedRequestBody) Close() error {
	body.closeOnce.Do(func() {
		body.closeErr = body.body.Close()
	})
	body.lifecycle.requestFinished()
	return body.closeErr
}

type trackedResponseBody struct {
	body      io.ReadCloser
	ctx       context.Context
	lifecycle *requestLifecycle
}

func (body *trackedResponseBody) Read(buffer []byte) (int, error) {
	n, err := body.body.Read(buffer)
	if err != nil {
		body.lifecycle.responseFinished()
		return n, classifyClose(body.ctx, err)
	}
	return n, nil
}

func (body *trackedResponseBody) Close() error {
	err := body.body.Close()
	cause := context.Cause(body.ctx)
	body.lifecycle.responseFinished()
	if errors.Is(cause, ErrTransportClosed) {
		if err == nil {
			return ErrTransportClosed
		}
		return errors.Join(ErrTransportClosed, err)
	}
	return err
}

// CloseIdleConnections closes currently idle connections without interrupting
// active request or response bodies.
func (t *Transport) CloseIdleConnections() {
	if t != nil && t.base != nil {
		t.base.CloseIdleConnections()
	}
}

// Close fully closes the Transport. It is idempotent, cancels active requests
// and response bodies, closes idle connections, and rejects new requests.
func (t *Transport) Close() error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	operations := make([]activeOperation, 0, len(t.active))
	for _, operation := range t.active {
		operations = append(operations, operation)
	}
	t.stats.closed.Store(true)
	t.mu.Unlock()
	for _, operation := range operations {
		operation.cancel(ErrTransportClosed)
	}
	for _, operation := range operations {
		if operation.body != nil {
			_ = operation.body.Close()
		}
	}
	if t.base != nil {
		t.base.CloseIdleConnections()
	}
	return nil
}

// Stats returns a lock-free snapshot of transport-scoped counters.
func (t *Transport) Stats() Stats {
	if t == nil {
		return Stats{Closed: true}
	}
	active := t.stats.active.Load()
	if active < 0 {
		active = 0
	}
	return Stats{
		RoundTrips: t.stats.roundTrips.Load(),
		Responses:  t.stats.responses.Load(),
		Failed:     t.stats.failed.Load(),
		Completed:  t.stats.completed.Load(),
		Active:     uint64(active),
		Dials:      t.stats.dials.Load(),
		DialErrors: t.stats.dialErrors.Load(),
		Closed:     t.stats.closed.Load(),
	}
}

var _ http.RoundTripper = (*Transport)(nil)
