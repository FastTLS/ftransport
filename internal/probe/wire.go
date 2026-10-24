// Package probe contains narrow F0 wire probes. It is not a production
// protocol implementation and is intentionally unavailable to consumers.
package probe

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/FastTLS/ftransport/internal/xnet/http2"
)

const clientPreface = "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"

// HeaderField is one HTTP/1.1 field written in the exact slice order.
type HeaderField struct {
	Name  string
	Value string
}

// InitialPriority is one initial HTTP/2 PRIORITY frame. Weight is already in
// its one-byte wire representation (the API-facing value minus one).
type InitialPriority struct {
	StreamID uint32
	http2.PriorityParam
}

// WriteOrderedH1Request writes one deliberately small HTTP/1.1 request. This
// helper exists only to prove that http.Client can drive an ordered wire codec.
func WriteOrderedH1Request(w io.Writer, method, target, host string, fields []HeaderField) error {
	if !validToken(method) {
		return fmt.Errorf("probe: invalid method %q", method)
	}
	if target == "" || containsNewline(target) || containsNewline(host) {
		return errors.New("probe: invalid request target or host")
	}
	var request bytes.Buffer
	request.WriteString(method)
	request.WriteByte(' ')
	request.WriteString(target)
	request.WriteString(" HTTP/1.1\r\nHost: ")
	request.WriteString(host)
	request.WriteString("\r\n")
	for _, field := range fields {
		if !validToken(field.Name) || !validFieldValue(field.Value) {
			return fmt.Errorf("probe: invalid header %q", field.Name)
		}
		request.WriteString(field.Name)
		request.WriteString(": ")
		request.WriteString(field.Value)
		request.WriteString("\r\n")
	}
	request.WriteString("\r\n")
	_, err := w.Write(request.Bytes())
	return err
}

// WriteH2InitialFrames writes the client connection preface followed by one
// ordered SETTINGS frame, an optional connection WINDOW_UPDATE, and ordered
// PRIORITY frames.
func WriteH2InitialFrames(w io.Writer, settings []http2.Setting, connectionWindowIncrement uint32, priorities []InitialPriority) error {
	if connectionWindowIncrement > 0x7fffffff {
		return errors.New("probe: connection window increment exceeds 31 bits")
	}
	if _, err := io.WriteString(w, clientPreface); err != nil {
		return err
	}
	framer := http2.NewFramer(w, nil)
	if err := framer.WriteSettings(settings...); err != nil {
		return err
	}
	if connectionWindowIncrement != 0 {
		if err := framer.WriteWindowUpdate(0, connectionWindowIncrement); err != nil {
			return err
		}
	}
	for _, priority := range priorities {
		if err := framer.WritePriority(priority.StreamID, priority.PriorityParam); err != nil {
			return err
		}
	}
	return nil
}

func containsNewline(value string) bool {
	return strings.ContainsAny(value, "\r\n")
}

func validToken(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if !strings.ContainsRune("!#$%&'*+-.^_`|~", rune(c)) &&
			!(c >= '0' && c <= '9') &&
			!(c >= 'a' && c <= 'z') &&
			!(c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}

func validFieldValue(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] == '\t' {
			continue
		}
		if value[i] < 0x20 || value[i] == 0x7f {
			return false
		}
	}
	return true
}
