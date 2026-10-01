package wlgows

import (
	"bufio"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"
)

/*
startPingLoop is what each tick does; that it ticks at all is Loop's own test.
So base_di's loop has the same shape and no clock: it runs the trigger straight
through, checking for a stop after each call the way Loop does, which makes
these deterministic rather than a race against an interval.
*/
func TestStartPingLoop(t *testing.T) {
	// base_di runs up to 10 ticks on a real Conn, and its sendPing succeeds;
	// each case overrides what it looks at, and has to stop the loop.
	base_di := func(t *testing.T) startPingLoopDI {
		netConn := newFakeConn(nil)
		conn := NewConn(netConn, bufio.NewReader(netConn), false)
		conn.di.loop = func(trigger func(stop_signal chan<- struct{}), interval time.Duration) {
			stop_signal := make(chan struct{}, 1)
			for range 10 {
				trigger(stop_signal)
				select {
				case <-stop_signal:
					return
				default:
				}
			}
			t.Error("the trigger never stopped")
		}
		return startPingLoopDI{
			conn:     conn,
			sendPing: func(payloadData []byte) error { return nil },
		}
	}

	/*
		5.5.2 has the peer echo the payload, so payload is called per ping rather
		than once and reused — that is what lets a Pong hook tell them apart. A
		ping that fails is the last one: nothing retries.
	*/
	t.Run("pings each tick with its own payload until one fails", func(t *testing.T) {
		var sent []string
		di := base_di(t)
		di.sendPing = func(payloadData []byte) error {
			sent = append(sent, string(payloadData))
			if len(sent) == 3 {
				return errors.New("socket gone")
			}
			return nil
		}

		pings := 0
		startPingLoop(30*time.Second, func() []byte {
			pings++
			return []byte(fmt.Sprintf("ping-%d", pings))
		}, di)

		if !slices.Equal(sent, []string{"ping-1", "ping-2", "ping-3"}) {
			t.Errorf("pinged %q, want [ping-1 ping-2 ping-3] and then stop", sent)
		}
	})

	t.Run("hands the interval to the loop", func(t *testing.T) {
		var gotInterval time.Duration
		di := base_di(t)
		di.conn.di.loop = func(_ func(chan<- struct{}), interval time.Duration) { gotInterval = interval }

		startPingLoop(30*time.Second, nil, di)

		if gotInterval != 30*time.Second {
			t.Errorf("interval = %v, want 30s", gotInterval)
		}
	})

	// nil is the ordinary heartbeat, and it must not turn into a payload of its own.
	t.Run("a nil payload pings empty", func(t *testing.T) {
		var sent [][]byte
		di := base_di(t)
		di.sendPing = func(payloadData []byte) error {
			sent = append(sent, payloadData)
			return errors.New("socket gone")
		}

		startPingLoop(time.Second, nil, di)

		if len(sent) != 1 || len(sent[0]) != 0 {
			t.Errorf("pinged %q, want one empty ping", sent)
		}
	})

	// Sent, not completed: this is the flag SendClose sets, so it covers both
	// sides of the handshake — closing first, or a Close hook answering the
	// peer. Without it a heartbeat outlives the conversation it was there to
	// check.
	t.Run("stops once a close is sent, reading it under the write lock", func(t *testing.T) {
		writeLocker := &fakeLocker{}
		di := base_di(t)
		di.conn.closeSent = true
		di.conn.di.writeLocker = writeLocker
		di.sendPing = func([]byte) error {
			t.Error("pinged after a close")
			return nil
		}

		startPingLoop(time.Second, nil, di)

		if !writeLocker.ok(1) {
			t.Errorf("write locks=%d unlocks=%d, want 1/1", writeLocker.locks, writeLocker.unlocks)
		}
	})
}
