//go:build !ftransport_http2_stdwrap

package http2

import (
	"context"
	"sync"
	"testing"
)

func TestConfiguredFirstStreamLifecycle(t *testing.T) {
	cc := &ClientConn{firstStreamID: 3, nextStreamID: 3, singleUse: true, maxConcurrentStreams: 100}
	if !cc.idleState().canTakeNewRequest {
		t.Fatal("fresh single-use connection rejected")
	}
	cc.nextStreamID = 5
	if cc.idleState().canTakeNewRequest {
		t.Fatal("single-use connection reused")
	}
	cc.singleUse = false
	cc.nextStreamID = 3
	cc.closed = true
	if !cc.idleState().canTakeNewRequest {
		t.Fatal("fresh failure hidden")
	}
	if err := cc.awaitOpenSlotForStreamLocked(nil); err != errClientConnNotEstablished {
		t.Fatalf("fresh failure: %v", err)
	}
	cc.closedOnIdle = true
	if cc.idleState().canTakeNewRequest {
		t.Fatal("idle-closed connection reused")
	}
	cc.closed = false
	cc.nextStreamID = 0x7fffffff
	if !cc.isUsableLocked() {
		t.Fatal("last valid stream rejected")
	}
	cc.nextStreamID += 2
	if cc.isUsableLocked() {
		t.Fatal("stream IDs exhausted but connection reusable")
	}
}

func TestConfiguredFirstStreamGoAway(t *testing.T) {
	for _, code := range []ErrCode{ErrCodeNo, ErrCodeProtocol} {
		cc := &ClientConn{firstStreamID: 3, nextStreamID: 5, streams: make(map[uint32]*clientStream)}
		cc.cond = sync.NewCond(&cc.mu)
		cs := &clientStream{cc: cc, ID: 3, ctx: context.Background(), abort: make(chan struct{})}
		cc.streams[3] = cs
		cc.setGoAway(&GoAwayFrame{FrameHeader: FrameHeader{valid: true}, LastStreamID: 0, ErrCode: code})
		if cs.abortErr == nil {
			t.Fatal("GOAWAY did not abort stream")
		}
		if (cs.abortErr == errClientConnGotGoAway) != (code == ErrCodeNo) {
			t.Fatalf("retry classification: %v", cs.abortErr)
		}
	}
}
