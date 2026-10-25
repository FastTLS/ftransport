package ftransport

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type contextRecordingTransport struct {
	idleRoundTripper
	contexts chan context.Context
}

type lifecycleRoundTripper func(*http.Request) (*http.Response, error)

func (roundTrip lifecycleRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func (lifecycleRoundTripper) CloseIdleConnections() {}

func TestDuplexContextReleasedOnlyAfterBothDirectionsFinish(t *testing.T) {
	for _, responseFirst := range []bool{false, true} {
		name := "upload-first"
		if responseFirst {
			name = "response-first"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			transport, err := NewTransport(Config{})
			if err != nil {
				t.Fatal(err)
			}
			defer transport.Close()
			var sent *http.Request
			transport.base = lifecycleRoundTripper(func(request *http.Request) (*http.Response, error) {
				sent = request
				return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
			})
			reader, writer := io.Pipe()
			defer writer.Close()
			defer reader.Close()
			request, err := http.NewRequest(http.MethodPost, "https://example.com/", reader)
			if err != nil {
				t.Fatal(err)
			}
			response, err := transport.RoundTrip(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			finishResponse := func() {
				t.Helper()
				if _, err := io.Copy(io.Discard, response.Body); err != nil {
					t.Fatal(err)
				}
			}
			finishUpload := func() {
				t.Helper()
				if err := sent.Body.Close(); err != nil {
					t.Fatal(err)
				}
			}
			first, second := finishUpload, finishResponse
			if responseFirst {
				first, second = finishResponse, finishUpload
			}
			first()
			if err := sent.Context().Err(); err != nil {
				t.Fatalf("unfinished duplex direction was canceled: %v", err)
			}
			if stats := transport.Stats(); stats.Active != 1 || stats.Completed != 0 {
				t.Fatalf("unfinished duplex stats = %#v", stats)
			}
			second()
			assertContextReleased(t, sent.Context())
			if stats := transport.Stats(); stats.Active != 0 || stats.Completed != 1 {
				t.Fatalf("completed duplex stats = %#v", stats)
			}
		})
	}
}

func (transport *contextRecordingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.contexts <- request.Context()
	return transport.idleRoundTripper.RoundTrip(request)
}

func TestCompletedRequestsReleaseContexts(t *testing.T) {
	for _, protocol := range []string{"default", "h1", "h2"} {
		for _, completion := range []string{"eof", "close", "error"} {
			t.Run(protocol+"/"+completion, func(t *testing.T) {
				t.Parallel()
				server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					_, _ = io.WriteString(w, "response body")
				}))
				var config Config
				switch protocol {
				case "h1":
					config.Protocols = []Protocol{ProtocolHTTP1}
				case "h2":
					config.Protocols = []Protocol{ProtocolHTTP2}
					server.EnableHTTP2 = true
				}
				if protocol == "h2" {
					server.StartTLS()
					config.TLSConfig = verifiedTestTLSConfig(t, server)
				} else {
					server.Start()
				}
				defer server.Close()
				transport, err := NewTransport(config)
				if err != nil {
					t.Fatal(err)
				}
				defer transport.Close()
				contexts := make(chan context.Context, 1)
				transport.base = &contextRecordingTransport{transport.base, contexts}
				parent, cancel := context.WithCancel(context.Background())
				defer cancel()
				client := &http.Client{Transport: transport}
				for range 8 {
					request, err := http.NewRequestWithContext(parent, http.MethodGet, server.URL, nil)
					if err != nil {
						t.Fatal(err)
					}
					if completion == "error" {
						request.Header["Invalid Header"] = []string{"value"}
					}
					response, err := client.Do(request)
					if completion == "error" {
						if err == nil {
							_ = response.Body.Close()
							t.Fatal("invalid request unexpectedly succeeded")
						}
					} else {
						if err != nil {
							t.Fatal(err)
						}
						if completion == "eof" {
							if _, err := io.Copy(io.Discard, response.Body); err != nil {
								t.Fatal(err)
							}
							// EOF alone must release the context, before Body.Close.
							assertContextReleased(t, <-contexts)
						}
						if err := response.Body.Close(); err != nil {
							t.Fatal(err)
						}
					}
					if completion != "eof" {
						assertContextReleased(t, <-contexts)
					}
					if err := parent.Err(); err != nil {
						t.Fatalf("request cleanup canceled caller context: %v", err)
					}
				}
				if stats := transport.Stats(); stats.Active != 0 || stats.Completed != 8 {
					t.Fatalf("completed request stats = %#v", stats)
				}
			})
		}
	}
}

func assertContextReleased(t *testing.T, ctx context.Context) {
	t.Helper()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("completed request retains a live transport context")
	}
}

func TestCleartextHTTP1HasNoTLSState(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()
	transport, err := NewTransport(Config{Protocols: []Protocol{ProtocolHTTP1}})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	client := &http.Client{Transport: transport}
	for range 2 {
		response, err := client.Get(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		_, readErr := io.Copy(io.Discard, response.Body)
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil {
			t.Fatalf("response body: read=%v close=%v", readErr, closeErr)
		}
		if response.TLS != nil {
			t.Fatal("cleartext response contains TLS state")
		}
	}
	if dials := transport.Stats().Dials; dials != 1 {
		t.Fatalf("connection reuse lost: dials=%d", dials)
	}
}
