package wlgows

import (
	"testing"
	"time"
)

/*
SetConfig and GetConfig are the only ways in and out of l.config, so between
them they carry the whole guarantee: the lock is taken, and what crosses is a
copy rather than a reference the other side can still reach.
*/

// All of it, every time — what is left out is set to its zero value, not left
// alone. Zero is a real setting for each: no timeout, no limit, a peer that is
// a server, hooks that drop.
func TestListenerSetConfig(t *testing.T) {
	locker := &fakeLocker{}
	l := &Listener{configLocker: locker}
	called := 0

	l.SetConfig(ListenerConfig{
		PeerIsClient:         true,
		MaxMsgPayloadByteLen: 1000,
		MaxMsgFrameCount:     4000,
		FrameReadTimeout:     30 * time.Second,
		Text:                 func(Frames) { called++ },
	})

	if !locker.ok(1) {
		t.Errorf("locks=%d unlocks=%d misuse=%d", locker.locks, locker.unlocks, locker.misuse)
	}
	if !l.config.PeerIsClient || l.config.MaxMsgPayloadByteLen != 1000 ||
		l.config.MaxMsgFrameCount != 4000 || l.config.FrameReadTimeout != 30*time.Second {
		t.Errorf("config = %+v, want what was set", l.config)
	}
	l.routeFrame(&Frame{Opcode: OpcodeText, FIN: true, PayloadData: []byte("hi")})
	if called != 1 {
		t.Errorf("the Text hook ran %d times, want 1", called)
	}

	l.SetConfig(ListenerConfig{})

	if l.config.MaxMsgPayloadByteLen != 0 || l.config.Text != nil {
		t.Error("the previous config survived being replaced")
	}
	// A hook replaced by nil drops the frames rather than being called through.
	if err := l.routeFrame(&Frame{Opcode: OpcodeText, FIN: true, PayloadData: []byte("hi")}); err != nil {
		t.Errorf("a message with no hook should be dropped: %v", err)
	}
	if called != 1 {
		t.Errorf("the replaced hook ran again, %d times total", called)
	}
}

// A copy, taken under the lock. The copy is what makes it safe to hand out: the
// caller cannot reach l.config through it, and SetConfig cannot reach theirs.
func TestListenerGetConfig(t *testing.T) {
	locker := &fakeLocker{}
	l := &Listener{configLocker: locker}
	l.SetConfig(ListenerConfig{MaxMsgFrameCount: 4000, Text: func(Frames) {}})
	locker.locks, locker.unlocks = 0, 0 // count this call only

	got := l.GetConfig()

	if got.MaxMsgFrameCount != 4000 || got.Text == nil {
		t.Errorf("got %+v, want what was set", got)
	}
	if !locker.ok(1) {
		t.Errorf("locks=%d unlocks=%d misuse=%d", locker.locks, locker.unlocks, locker.misuse)
	}

	got.MaxMsgFrameCount = 1

	if l.config.MaxMsgFrameCount != 4000 {
		t.Error("writing to the returned config reached the Listener")
	}
}
