package wlgows

import (
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"
)

// A copy, taken under the lock. The copy is what makes it safe to hand out: the
// caller cannot reach l.config through it, and SetConfig cannot reach theirs.
func TestListenerGetConfig(t *testing.T) {
	var steps []string
	l := &Listener{configLocker: &sync.Mutex{}}
	l.SetConfig(ListenerConfig{MaxDataFrameCount: 4000, Text: func(DataFrames) {}})
	l.configLocker = fakeFuncLocker{
		lock:   func() { steps = append(steps, "config lock") },
		unlock: func() { steps = append(steps, "config unlock") },
	}

	got := l.GetConfig()

	if got.MaxDataFrameCount != 4000 || got.Text == nil {
		t.Errorf("got %+v, want what was set", got)
	}
	if want := []string{"config lock", "config unlock"}; !slices.Equal(steps, want) {
		t.Errorf("steps %q, want %q", steps, want)
	}

	got.MaxDataFrameCount = 1

	if l.config.MaxDataFrameCount != 4000 {
		t.Error("writing to the returned config reached the Listener")
	}
}

/*
SetConfig and GetConfig are the only ways in and out of l.config, so between
them they carry the whole guarantee: the lock is taken, and what crosses is a
copy rather than a reference the other side can still reach.
*/

// All of it, every time — what is left out is set to its zero value, not left
// alone. Zero is a real setting for each: no timeout, no limit, a peer that is
// a server, hooks that drop.
func TestListenerSetConfig(t *testing.T) {
	var steps []string
	l := &Listener{}
	l.configLocker = fakeFuncLocker{
		lock: func() { steps = append(steps, "config lock") },
		unlock: func() {
			steps = append(steps, fmt.Sprintf("config unlock frames=%d", l.config.MaxDataFrameCount))
		},
	}
	called := 0

	l.SetConfig(ListenerConfig{
		PeerIsClient:      true,
		MaxDataFramesSize: 1000,
		MaxDataFrameCount: 4000,
		FrameReadTimeout:  30 * time.Second,
		Text:              func(DataFrames) { called++ },
	})

	// The config is replaced before the unlock.
	if want := []string{"config lock", "config unlock frames=4000"}; !slices.Equal(steps, want) {
		t.Errorf("steps %q, want %q", steps, want)
	}
	if !l.config.PeerIsClient || l.config.MaxDataFramesSize != 1000 ||
		l.config.MaxDataFrameCount != 4000 || l.config.FrameReadTimeout != 30*time.Second {
		t.Errorf("config = %+v, want what was set", l.config)
	}
	l.routeFrame(&Frame{Opcode: OpcodeText, FIN: true, PayloadData: []byte("hi")})
	if called != 1 {
		t.Errorf("the Text hook ran %d times, want 1", called)
	}

	l.SetConfig(ListenerConfig{})

	if l.config.MaxDataFramesSize != 0 || l.config.Text != nil {
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
