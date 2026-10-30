package ftransport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FastTLS/ftransport/internal/xnet/http2"
	"github.com/FastTLS/ftransport/internal/xnet/http2/hpack"
)

func TestHTTP2StreamConfigurationValidation(t *testing.T) {
	for _, id := range []uint32{0, 1, 3, 0x7fffffff} {
		if err := (Config{HTTP2: HTTP2Config{FirstStreamID: id}}).Validate(); err != nil {
			t.Fatal(id, err)
		}
	}
	for _, c := range []HTTP2Config{
		{FirstStreamID: 2}, {FirstStreamID: 0x80000001},
		{StreamWindowIncrement: 0x7fffffff},
		{InitialSettings: ExplicitHTTP2Settings(), StreamWindowIncrement: 0x7fffffff - 65535 + 1},
		{InitialSettings: ExplicitHTTP2Settings(HTTP2Setting{ID: SettingInitialWindowSize, Value: 131072}), StreamWindowIncrement: 0x7fffffff - 131072 + 1},
	} {
		if err := (Config{HTTP2: c}).Validate(); err == nil {
			t.Fatalf("accepted %+v", c)
		}
	}
	if err := (Config{HTTP2: HTTP2Config{InitialSettings: ExplicitHTTP2Settings(HTTP2Setting{ID: SettingInitialWindowSize, Value: 0}), StreamWindowIncrement: 0x7fffffff}}).Validate(); err != nil {
		t.Fatal(err)
	}
	for _, c := range []HTTP2Config{{FirstStreamID: 3}, {StreamWindowIncrement: 1}} {
		if _, err := NewTransport(Config{HTTP2: c}); err == nil {
			t.Fatal("wire config silently negotiated")
		}
	}
}

// A raw peer deliberately sends more than SETTINGS_INITIAL_WINDOW_SIZE, so a
// cosmetic WINDOW_UPDATE without matching receive credit fails this test.
func TestHTTP2StreamConfigurationWireAndReceiveCredit(t *testing.T) {
	for _, first := range []uint32{0, 3, 0x7fffffff} {
		t.Run(fmt.Sprint(first), func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			wantFirst := first
			if wantFirst == 0 {
				wantFirst = 1
			}
			payload := bytes.Repeat([]byte("stream-credit"), 20000)
			serverResult := make(chan error, 1)
			go func() {
				for connection := 0; connection < 2; connection++ {
					conn, err := listener.Accept()
					if err != nil {
						serverResult <- err
						return
					}
					err = serveStreamCredit(conn, wantFirst, payload, first == 0x7fffffff)
					conn.Close()
					if err != nil {
						serverResult <- err
						return
					}
				}
				serverResult <- nil
			}()
			tr, err := NewTransport(Config{Protocols: []Protocol{ProtocolHTTP2}, HTTP2: HTTP2Config{
				FirstStreamID: first, StreamWindowIncrement: 524288, ConnectionWindowIncrement: 1048576,
				InitialSettings: ExplicitHTTP2Settings(HTTP2Setting{ID: SettingEnablePush, Value: 0}, HTTP2Setting{ID: SettingInitialWindowSize, Value: 131072}),
			}, DialContext: func(ctx context.Context, _ DialRequest) (DialResult, error) {
				conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", listener.Addr().String())
				return DialResult{Conn: conn, NegotiatedProtocol: "h2", TLSState: &tls.ConnectionState{HandshakeComplete: true, NegotiatedProtocol: "h2", NegotiatedProtocolIsMutual: true, Version: tls.VersionTLS13}}, err
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer tr.Close()
			count := 2
			if first == 0x7fffffff {
				count = 1
			}
			for connection := 0; connection < 2; connection++ {
				for request := 0; request < count; request++ {
					ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
					req, _ := http.NewRequestWithContext(ctx, "GET", "https://stream.test/", nil)
					response, err := tr.RoundTrip(req)
					if err != nil {
						cancel()
						t.Fatal(err)
					}
					body, err := io.ReadAll(response.Body)
					response.Body.Close()
					cancel()
					if err != nil || sha256.Sum256(body) != sha256.Sum256(payload) {
						t.Fatalf("body: len=%d err=%v", len(body), err)
					}
				}
				tr.CloseIdleConnections()
			}
			if err := <-serverResult; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func serveStreamCredit(conn net.Conn, first uint32, payload []byte, exhausted bool) error {
	conn.SetDeadline(time.Now().Add(8 * time.Second))
	preface := make([]byte, len(http2.ClientPreface))
	if _, err := io.ReadFull(conn, preface); err != nil {
		return err
	}
	if string(preface) != http2.ClientPreface {
		return errors.New("bad preface")
	}
	fr := http2.NewFramer(conn, conn)
	if err := fr.WriteSettings(); err != nil {
		return err
	}
	count := 2
	if exhausted {
		count = 1
	}
	for n := 0; n < count; n++ {
		want := first + uint32(2*n)
		headers := false
		for {
			frame, err := fr.ReadFrame()
			if err != nil {
				return err
			}
			switch f := frame.(type) {
			case *http2.SettingsFrame:
				if !f.IsAck() {
					if err := fr.WriteSettingsAck(); err != nil {
						return err
					}
				}
			case *http2.HeadersFrame:
				if headers || f.StreamID != want || !f.HeadersEnded() {
					return fmt.Errorf("headers stream=%d want=%d", f.StreamID, want)
				}
				headers = true
			case *http2.WindowUpdateFrame:
				if f.StreamID != want {
					continue
				}
				if f.Increment != 524288 {
					continue
				} // later read-driven updates may also arrive
				if !headers {
					return errors.New("stream update before HEADERS")
				}
				goto respond
			}
		}
	respond:
		var block bytes.Buffer
		encoder := hpack.NewEncoder(&block)
		encoder.WriteField(hpack.HeaderField{Name: ":status", Value: "200"})
		encoder.WriteField(hpack.HeaderField{Name: "content-length", Value: fmt.Sprint(len(payload))})
		if err := fr.WriteHeaders(http2.HeadersFrameParam{StreamID: want, BlockFragment: block.Bytes(), EndHeaders: true}); err != nil {
			return err
		}
		for offset := 0; offset < len(payload); {
			end := min(offset+16300, len(payload))
			if err := fr.WriteDataPadded(want, end == len(payload), payload[offset:end], make([]byte, 10)); err != nil {
				return err
			}
			offset = end
		}
	}
	// Keep the connection alive until the client explicitly retires it.
	for {
		if _, err := fr.ReadFrame(); err != nil {
			return nil
		}
	}
}

func TestHTTP2ConfiguredFirstStreamGoAwayRetry(t *testing.T) {
	for _, code := range []http2.ErrCode{http2.ErrCodeNo, http2.ErrCodeProtocol} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			var dials atomic.Int32
			tr, err := NewTransport(Config{Protocols: []Protocol{ProtocolHTTP2}, HTTP2: HTTP2Config{FirstStreamID: 3},
				DialContext: func(ctx context.Context, _ DialRequest) (DialResult, error) {
					attempt := dials.Add(1)
					client, server := net.Pipe()
					go func() {
						defer server.Close()
						server.SetDeadline(time.Now().Add(5 * time.Second))
						preface := make([]byte, len(http2.ClientPreface))
						if _, err := io.ReadFull(server, preface); err != nil {
							return
						}
						fr := http2.NewFramer(server, server)
						// Read client preface before writing: net.Pipe has no buffering.
						if _, err := fr.ReadFrame(); err != nil {
							return
						}
						if fr.WriteSettings() != nil {
							return
						}
						for {
							f, err := fr.ReadFrame()
							if err != nil {
								return
							}
							if h, ok := f.(*http2.HeadersFrame); ok {
								if h.StreamID != 3 {
									return
								}
								if attempt == 1 {
									fr.WriteGoAway(0, code, nil)
									return
								}
								fr.WriteHeaders(http2.HeadersFrameParam{StreamID: 3, BlockFragment: []byte{0x88}, EndHeaders: true, EndStream: true})
								return
							}
						}
					}()
					return DialResult{Conn: client, NegotiatedProtocol: "h2", TLSState: &tls.ConnectionState{HandshakeComplete: true, NegotiatedProtocol: "h2", NegotiatedProtocolIsMutual: true}}, nil
				}})
			if err != nil {
				t.Fatal(err)
			}
			defer tr.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, "GET", "https://goaway.test/", nil)
			resp, err := tr.RoundTrip(req)
			if code == http2.ErrCodeNo {
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
				if dials.Load() != 2 {
					t.Fatalf("retry dials=%d", dials.Load())
				}
			} else if err == nil || dials.Load() != 1 {
				t.Fatalf("fatal GOAWAY err=%v dials=%d", err, dials.Load())
			}
		})
	}
}
