package ftransport

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"slices"
	"time"
)

// Protocol identifies an application protocol a Transport may negotiate.
type Protocol uint8

const (
	ProtocolHTTP1 Protocol = iota + 1
	ProtocolHTTP2
)

func (p Protocol) String() string {
	switch p {
	case ProtocolHTTP1:
		return "http/1.1"
	case ProtocolHTTP2:
		return "h2"
	default:
		return fmt.Sprintf("Protocol(%d)", p)
	}
}

// DialResult transfers ownership of exactly one established connection to the
// transport. NegotiatedProtocol and TLSState describe an already-completed TLS
// handshake; both are empty for a clear-text connection.
//
// A custom dialer is responsible for proxying, TLS verification, hostname
// verification, and the handshake. The transport must close Conn exactly once
// after a successful handoff.
type DialResult struct {
	Conn               net.Conn
	NegotiatedProtocol string
	TLSState           *tls.ConnectionState
}

// DialRequest describes one connection requested by the transport. Slices are
// owned by the caller for the duration of DialContextFunc and must not be
// retained or modified.
type DialRequest struct {
	Network       string
	Address       string
	TLS           bool
	ServerName    string
	ALPNProtocols []string
}

// DialContextFunc establishes the connection used by one transport dial. For
// TLS requests it must complete the handshake and certificate verification
// before returning.
type DialContextFunc func(ctx context.Context, request DialRequest) (DialResult, error)

// Config is immutable after it is passed to a future Transport constructor.
// The constructor will validate and deep-copy it.
type Config struct {
	DialContext DialContextFunc
	TLSConfig   *tls.Config
	Protocols   []Protocol
	HTTP1       HTTP1Config
	HTTP2       HTTP2Config
	Pool        PoolConfig
}

// HTTP1Config controls connection-identity HTTP/1.1 encoding behavior.
type HTTP1Config struct {
	HeaderOrder []string
}

// HTTP2Config controls the frames sent when a new HTTP/2 connection starts and
// the connection-identity header encoding behavior.
type HTTP2Config struct {
	InitialSettings           HTTP2Settings
	ConnectionWindowIncrement uint32
	PriorityFrames            []HTTP2PriorityFrame
	HeaderPriority            *HTTP2PriorityParam
	PseudoHeaderOrder         []PseudoHeader
	HeaderOrder               []string
	AllowServerPush           bool
}

// HTTP2Settings distinguishes an unspecified list (Present is false) from an
// explicitly empty SETTINGS frame (Present is true and Values is empty).
type HTTP2Settings struct {
	Present bool
	Values  []HTTP2Setting
}

// ExplicitHTTP2Settings returns an explicitly configured SETTINGS list. With
// no arguments it represents an empty initial SETTINGS frame.
func ExplicitHTTP2Settings(settings ...HTTP2Setting) HTTP2Settings {
	values := make([]HTTP2Setting, len(settings))
	copy(values, settings)
	return HTTP2Settings{Present: true, Values: values}
}

// HTTP2Setting is one ordered HTTP/2 SETTINGS entry.
type HTTP2Setting struct {
	ID    HTTP2SettingID
	Value uint32
}

// HTTP2SettingID is the 16-bit identifier encoded in a SETTINGS entry.
type HTTP2SettingID uint16

const (
	SettingHeaderTableSize       HTTP2SettingID = 0x1
	SettingEnablePush            HTTP2SettingID = 0x2
	SettingMaxConcurrentStreams  HTTP2SettingID = 0x3
	SettingInitialWindowSize     HTTP2SettingID = 0x4
	SettingMaxFrameSize          HTTP2SettingID = 0x5
	SettingMaxHeaderListSize     HTTP2SettingID = 0x6
	SettingEnableConnectProtocol HTTP2SettingID = 0x8
	SettingNoRFC7540Priorities   HTTP2SettingID = 0x9
)

// HTTP2PriorityParam uses the RFC-facing weight range 1..256. An encoder must
// write Weight-1 into the one-byte wire field.
type HTTP2PriorityParam struct {
	Dependency uint32
	Weight     uint16
	Exclusive  bool
}

// HTTP2PriorityFrame describes one initial PRIORITY frame.
type HTTP2PriorityFrame struct {
	StreamID uint32
	HTTP2PriorityParam
}

// PseudoHeader is a request pseudo-header name.
type PseudoHeader string

const (
	PseudoHeaderMethod    PseudoHeader = ":method"
	PseudoHeaderAuthority PseudoHeader = ":authority"
	PseudoHeaderScheme    PseudoHeader = ":scheme"
	PseudoHeaderPath      PseudoHeader = ":path"
	PseudoHeaderProtocol  PseudoHeader = ":protocol"
)

// PoolConfig controls connection capacity and idle retirement. Zero values use
// documented implementation defaults; negative values are invalid.
type PoolConfig struct {
	MaxConnectionsPerHost int
	MaxIdleConnections    int
	MaxIdlePerHost        int
	IdleConnectionTimeout time.Duration
}

// Clone returns a deep copy suitable for retaining as immutable transport
// identity. Function and connection values are intentionally shared.
func (c Config) Clone() Config {
	c.Protocols = slices.Clone(c.Protocols)
	c.HTTP1.HeaderOrder = slices.Clone(c.HTTP1.HeaderOrder)
	c.HTTP2.InitialSettings.Values = slices.Clone(c.HTTP2.InitialSettings.Values)
	c.HTTP2.PriorityFrames = slices.Clone(c.HTTP2.PriorityFrames)
	c.HTTP2.PseudoHeaderOrder = slices.Clone(c.HTTP2.PseudoHeaderOrder)
	c.HTTP2.HeaderOrder = slices.Clone(c.HTTP2.HeaderOrder)
	if c.HTTP2.HeaderPriority != nil {
		priority := *c.HTTP2.HeaderPriority
		c.HTTP2.HeaderPriority = &priority
	}
	if c.TLSConfig != nil {
		c.TLSConfig = cloneTLSConfig(c.TLSConfig)
	}
	return c
}

func cloneTLSConfig(source *tls.Config) *tls.Config {
	if source == nil {
		return nil
	}
	clone := source.Clone()
	if source.RootCAs != nil {
		clone.RootCAs = source.RootCAs.Clone()
	}
	if source.ClientCAs != nil {
		clone.ClientCAs = source.ClientCAs.Clone()
	}
	clone.NextProtos = slices.Clone(source.NextProtos)
	clone.CipherSuites = slices.Clone(source.CipherSuites)
	clone.CurvePreferences = slices.Clone(source.CurvePreferences)
	clone.EncryptedClientHelloConfigList = slices.Clone(source.EncryptedClientHelloConfigList)
	clone.Certificates = slices.Clone(source.Certificates)
	for i := range source.Certificates {
		clone.Certificates[i] = cloneTLSCertificate(source.Certificates[i])
	}
	if source.NameToCertificate != nil {
		clone.NameToCertificate = make(map[string]*tls.Certificate, len(source.NameToCertificate))
		indices := make(map[*tls.Certificate]int, len(source.Certificates))
		for i := range source.Certificates {
			indices[&source.Certificates[i]] = i
		}
		for name, certificate := range source.NameToCertificate {
			if certificate == nil {
				clone.NameToCertificate[name] = nil
				continue
			}
			if index, ok := indices[certificate]; ok {
				clone.NameToCertificate[name] = &clone.Certificates[index]
				continue
			}
			certificateClone := cloneTLSCertificate(*certificate)
			clone.NameToCertificate[name] = &certificateClone
		}
	}
	clone.EncryptedClientHelloKeys = slices.Clone(source.EncryptedClientHelloKeys)
	for i := range clone.EncryptedClientHelloKeys {
		clone.EncryptedClientHelloKeys[i].Config = slices.Clone(source.EncryptedClientHelloKeys[i].Config)
		clone.EncryptedClientHelloKeys[i].PrivateKey = slices.Clone(source.EncryptedClientHelloKeys[i].PrivateKey)
	}
	return clone
}

func cloneTLSCertificate(source tls.Certificate) tls.Certificate {
	clone := source
	clone.Certificate = cloneByteSlices(source.Certificate)
	clone.SupportedSignatureAlgorithms = slices.Clone(source.SupportedSignatureAlgorithms)
	clone.OCSPStaple = slices.Clone(source.OCSPStaple)
	clone.SignedCertificateTimestamps = cloneByteSlices(source.SignedCertificateTimestamps)
	return clone
}

func cloneByteSlices(source [][]byte) [][]byte {
	clone := slices.Clone(source)
	for i := range source {
		clone[i] = slices.Clone(source[i])
	}
	return clone
}

// Validate checks configuration invariants that do not require network I/O.
func (c Config) Validate() error {
	if err := validateProtocols(c.Protocols); err != nil {
		return err
	}
	if err := validateHeaderOrder("http1 header order", c.HTTP1.HeaderOrder); err != nil {
		return err
	}
	if err := c.HTTP2.validate(); err != nil {
		return err
	}
	if c.Pool.MaxConnectionsPerHost < 0 || c.Pool.MaxIdleConnections < 0 || c.Pool.MaxIdlePerHost < 0 {
		return errors.New("ftransport: pool limits must not be negative")
	}
	if c.Pool.IdleConnectionTimeout < 0 {
		return errors.New("ftransport: idle connection timeout must not be negative")
	}
	return nil
}

func validateProtocols(protocols []Protocol) error {
	seen := make(map[Protocol]struct{}, len(protocols))
	for _, protocol := range protocols {
		if protocol != ProtocolHTTP1 && protocol != ProtocolHTTP2 {
			return fmt.Errorf("ftransport: unsupported protocol %d", protocol)
		}
		if _, ok := seen[protocol]; ok {
			return fmt.Errorf("ftransport: duplicate protocol %s", protocol)
		}
		seen[protocol] = struct{}{}
	}
	return nil
}

func (c HTTP2Config) validate() error {
	if !c.InitialSettings.Present && c.InitialSettings.Values != nil {
		return errors.New("ftransport: HTTP/2 settings values require Present=true")
	}
	seenSettings := make(map[HTTP2SettingID]struct{}, len(c.InitialSettings.Values))
	for _, setting := range c.InitialSettings.Values {
		if setting.ID == 0 {
			return errors.New("ftransport: HTTP/2 setting ID 0 is reserved")
		}
		if _, ok := seenSettings[setting.ID]; ok {
			return fmt.Errorf("ftransport: duplicate HTTP/2 setting ID %d", setting.ID)
		}
		seenSettings[setting.ID] = struct{}{}
		if err := validateSetting(setting); err != nil {
			return err
		}
	}
	if c.ConnectionWindowIncrement > 0x7fffffff-65535 {
		return errors.New("ftransport: HTTP/2 connection window exceeds the 31-bit maximum")
	}
	for i, priority := range c.PriorityFrames {
		if priority.StreamID == 0 || priority.StreamID > 0x7fffffff || priority.StreamID%2 == 0 {
			return fmt.Errorf("ftransport: priority frame %d has invalid client stream ID %d", i, priority.StreamID)
		}
		if err := validatePriority(priority.HTTP2PriorityParam, priority.StreamID); err != nil {
			return fmt.Errorf("ftransport: priority frame %d: %w", i, err)
		}
	}
	if c.HeaderPriority != nil {
		if err := validatePriority(*c.HeaderPriority, 0); err != nil {
			return fmt.Errorf("ftransport: header priority: %w", err)
		}
		if c.HeaderPriority.Dependency%2 != 0 {
			return errors.New("ftransport: static header priority cannot depend on a client stream ID")
		}
	}
	if err := validatePseudoHeaderOrder(c.PseudoHeaderOrder); err != nil {
		return err
	}
	if err := validateHeaderOrder("http2 header order", c.HeaderOrder); err != nil {
		return err
	}
	if c.AllowServerPush {
		return errors.New("ftransport: server push is not supported in the first release")
	}
	return nil
}

func validateSetting(setting HTTP2Setting) error {
	switch setting.ID {
	case SettingEnablePush, SettingEnableConnectProtocol, SettingNoRFC7540Priorities:
		if setting.Value > 1 {
			return fmt.Errorf("ftransport: HTTP/2 setting %d must be 0 or 1", setting.ID)
		}
		if setting.ID == SettingEnablePush && setting.Value != 0 {
			return errors.New("ftransport: HTTP/2 server push is not supported")
		}
	case SettingInitialWindowSize:
		if setting.Value > 0x7fffffff {
			return errors.New("ftransport: HTTP/2 initial window exceeds 31 bits")
		}
	case SettingMaxFrameSize:
		if setting.Value < 1<<14 || setting.Value > (1<<24)-1 {
			return errors.New("ftransport: HTTP/2 max frame size is outside 16384..16777215")
		}
	}
	return nil
}

func validatePriority(priority HTTP2PriorityParam, streamID uint32) error {
	if priority.Dependency > 0x7fffffff {
		return errors.New("dependency exceeds 31 bits")
	}
	if streamID != 0 && priority.Dependency == streamID {
		return errors.New("stream cannot depend on itself")
	}
	if priority.Weight < 1 || priority.Weight > 256 {
		return errors.New("weight must be in 1..256")
	}
	return nil
}

func validatePseudoHeaderOrder(order []PseudoHeader) error {
	seen := make(map[PseudoHeader]struct{}, len(order))
	for _, header := range order {
		switch header {
		case PseudoHeaderMethod, PseudoHeaderAuthority, PseudoHeaderScheme, PseudoHeaderPath, PseudoHeaderProtocol:
		default:
			return fmt.Errorf("ftransport: invalid pseudo-header %q", header)
		}
		if _, ok := seen[header]; ok {
			return fmt.Errorf("ftransport: duplicate pseudo-header %q", header)
		}
		seen[header] = struct{}{}
	}
	return nil
}
