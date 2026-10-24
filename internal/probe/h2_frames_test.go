package probe

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/FastTLS/ftransport/internal/xnet/http2"
)

func TestH2InitialFramesMatchStaticWireVector(t *testing.T) {
	t.Parallel()

	settings := []http2.Setting{
		{ID: http2.SettingInitialWindowSize, Val: 0},
		{ID: http2.SettingHeaderTableSize, Val: 65536},
	}
	priorities := []InitialPriority{{
		StreamID: 3,
		PriorityParam: http2.PriorityParam{
			StreamDep: 0,
			Weight:    255,
		},
	}}
	var wire bytes.Buffer
	if err := WriteH2InitialFrames(&wire, settings, 1024, priorities); err != nil {
		t.Fatal(err)
	}

	// This literal is an independent byte vector, not output from a second
	// instance of the encoder under test.
	const expectedHex = "505249202a20485454502f322e300d0a0d0a534d0d0a0d0a" +
		"00000c040000000000000400000000000100010000" +
		"00000408000000000000000400" +
		"00000502000000000300000000ff"
	expected, err := hex.DecodeString(expectedHex)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(wire.Bytes(), expected) {
		t.Fatalf("initial frames mismatch\nwant: %x\n got: %x", expected, wire.Bytes())
	}
}
