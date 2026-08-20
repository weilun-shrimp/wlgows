package wlgows

import (
	"bufio"
	"errors"
	"fmt"
	"testing"
	"time"
)

/*
StartPingLoop is what each tick does; that it ticks at all is Loop's own test.
So the loop is substituted here — same shape, no clock — and these run the
trigger straight through, which is what makes them deterministic rather than a
race against an interval.

scriptedLoop calls the trigger until it asks to stop, checking after the call
the way Loop does. Running past the cap means the trigger never stopped.
*/
func scriptedLoop(t *testing.T, cap int, gotInterval *time.Duration, runs *int) func(func(chan<- struct{}), time.Duration) {
	t.Helper()
	return func(trigger func(stop_signal chan<- struct{}), interval time.Duration) {
		*gotInterval = interval
		stop_signal := make(chan struct{}, 1)
		for *runs < cap {
			*runs++
			trigger(stop_signal)

			select {
			case <-stop_signal:
				return
			default:
			}
		}
		t.Errorf("the trigger never stopped, ran %d times", *runs)
	}
}

func TestConnStartPingLoop(t *testing.T) {
	netConn := newFakeConn(nil)
	wsConn := NewConn(netConn, bufio.NewReader(netConn), false)

	writes, runs := 0, 0
	var gotInterval time.Duration
	netConn.onWrite = func() {
		writes++
		if writes == 3 { // the socket dies under the third ping
			netConn.writeErr = errors.New("socket gone")
		}
	}
	wsConn.di.loop = scriptedLoop(t, 10, &gotInterval, &runs)

	pings := 0
	wsConn.StartPingLoop(30*time.Second, func() []byte {
		pings++
		return []byte(fmt.Sprintf("ping-%d", pings))
	})

	// A ping that fails is the last one: nothing retries.
	if runs != 3 {
		t.Errorf("the trigger ran %d times, want 3 — it should stop on the failed send", runs)
	}
	if gotInterval != 30*time.Second {
		t.Errorf("interval = %v, want 30s", gotInterval)
	}

	frames := framesOn(t, netConn)
	if len(frames) != 2 {
		t.Fatalf("%d frames reached the socket, want 2 — the third failed", len(frames))
	}
	// 5.5.2 has the peer echo the payload, so payload is called per ping rather
	// than once and reused — that is what lets a Pong hook tell them apart.
	for i, f := range frames {
		if f.Opcode != OpcodePing {
			t.Errorf("frame %d opcode = %#x, want OpcodePing", i, f.Opcode)
		}
		if want := fmt.Sprintf("ping-%d", i+1); string(f.PayloadData) != want {
			t.Errorf("frame %d payload = %q, want %q", i, f.PayloadData, want)
		}
	}
}

// nil is the ordinary heartbeat, and it must not turn into a payload of its own.
func TestConnStartPingLoopNilPayload(t *testing.T) {
	netConn := newFakeConn(nil)
	wsConn := NewConn(netConn, bufio.NewReader(netConn), false)

	runs := 0
	var gotInterval time.Duration
	netConn.onWrite = func() { netConn.writeErr = errors.New("socket gone") }
	wsConn.di.loop = scriptedLoop(t, 10, &gotInterval, &runs)

	wsConn.StartPingLoop(time.Second, nil)

	frames := framesOn(t, netConn)
	if len(frames) != 0 {
		t.Fatalf("%d frames reached the socket, want 0 — the first write failed", len(frames))
	}
	if runs != 1 {
		t.Errorf("the trigger ran %d times, want 1", runs)
	}
}

// Sent, not completed: this is the flag SendClose sets, so it covers both sides
// of the handshake — closing first, or a Close hook answering the peer. Without
// it a heartbeat outlives the conversation it was there to check.
func TestConnStartPingLoopStopsOnceCloseSent(t *testing.T) {
	netConn := newFakeConn(nil)
	wsConn := NewConn(netConn, bufio.NewReader(netConn), false)
	if err := wsConn.SendClose(nil); err != nil {
		t.Fatalf("SendClose: %v", err)
	}
	sentByClose := len(netConn.written())

	runs := 0
	var gotInterval time.Duration
	wsConn.di.loop = scriptedLoop(t, 10, &gotInterval, &runs)

	wsConn.StartPingLoop(time.Second, nil)

	if runs != 1 {
		t.Errorf("the trigger ran %d times, want 1 — a close stops it on the first tick", runs)
	}
	if len(netConn.written()) != sentByClose {
		t.Error("a ping went out after the close")
	}
}
