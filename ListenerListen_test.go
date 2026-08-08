package wlgows

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

/*
listen is only the assembly of two halves, so this is only about the assembly:
claimListen, then listenFrames, then pauseListen — with all three stubbed. What
claimListen and listenFrames actually do is tested where they live.
*/
func TestListenerListen(t *testing.T) {
	t.Run("order", func(t *testing.T) {
		var steps []string
		claimed := make(chan struct{})
		var looped <-chan struct{}

		err := (&Listener{configLocker: &sync.Mutex{}}).listen(listenDI{
			claimListen: func() (chan struct{}, error) {
				steps = append(steps, "claimListen")
				return claimed, nil
			},
			listenFrames: func(pauseChan <-chan struct{}) error {
				steps = append(steps, "listenFrames")
				looped = pauseChan
				return nil
			},
			pauseListen: func() { steps = append(steps, "pauseListen") },
		})

		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		if got := strings.Join(steps, ","); got != "claimListen,listenFrames,pauseListen" {
			t.Errorf("steps = %s, want claimListen,listenFrames,pauseListen", got)
		}
		// listenFrames has to watch the channel claimListen just made, or
		// PauseListen closes one nothing is selecting on.
		if looped != claimed {
			t.Error("listenFrames was given a different channel than claimListen returned")
		}
	})

	// Nothing was claimed, so there is nothing for pauseListen to release and
	// nothing for listenFrames to read.
	t.Run("claimListen fails", func(t *testing.T) {
		want := errors.New("refused")
		var steps []string

		err := (&Listener{configLocker: &sync.Mutex{}}).listen(listenDI{
			claimListen:  func() (chan struct{}, error) { return nil, want },
			listenFrames: func(<-chan struct{}) error { steps = append(steps, "listenFrames"); return nil },
			pauseListen:  func() { steps = append(steps, "pauseListen") },
		})

		if !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
		if len(steps) != 0 {
			t.Errorf("ran %v after claimListen failed", steps)
		}
	})

	// pauseListen is deferred, so it has to run on the way out however
	// listenFrames ended — otherwise a failed run leaves the Listener claimed
	// forever.
	t.Run("listenFrames fails", func(t *testing.T) {
		want := errors.New("read failed")
		released := false

		err := (&Listener{configLocker: &sync.Mutex{}}).listen(listenDI{
			claimListen:  func() (chan struct{}, error) { return make(chan struct{}), nil },
			listenFrames: func(<-chan struct{}) error { return want },
			pauseListen:  func() { released = true },
		})

		if !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
		if !released {
			t.Error("pauseListen was never called")
		}
	})
}

/*
claimListen decides whether a run may start at all, and every path out of it
takes the lock and gives it back — a missed Unlock deadlocks the next caller
rather than failing, so the counting Locker is what makes it visible.
*/
func TestListenerClaimListen(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		locker := &fakeLocker{}
		l := &Listener{configLocker: &sync.Mutex{}, conn: &fakeListenerConn{}, listenLocker: locker}

		pauseChan, err := l.claimListen()

		if err != nil {
			t.Fatalf("claimListen: %v", err)
		}
		if pauseChan == nil {
			t.Fatal("no channel returned")
		}
		// The Listener has to hold the same channel, or PauseListen closes
		// something the run is not watching.
		if l.pauseChan != pauseChan {
			t.Error("the returned channel is not the one stored on the Listener")
		}
		if l.currentDataFrames == nil {
			t.Error("currentDataFrames should be ready for the first frame")
		}
		if !locker.ok(1) {
			t.Errorf("locks=%d unlocks=%d misuse=%d", locker.locks, locker.unlocks, locker.misuse)
		}
	})

	// pauseChan being set is what says a run already has the Listener.
	t.Run("already listening", func(t *testing.T) {
		running := make(chan struct{})
		locker := &fakeLocker{}
		l := &Listener{configLocker: &sync.Mutex{}, pauseChan: running, listenLocker: locker}

		// conn is left nil on purpose: the refusal has to come first, so a
		// Listener already running is never reported as unbuilt.
		_, err := l.claimListen()

		if !errors.Is(err, ErrListenerIsListening) {
			t.Errorf("err = %v, want ErrListenerIsListening", err)
		}
		if l.pauseChan != running {
			t.Error("the running channel was replaced")
		}
		if !locker.ok(1) {
			t.Errorf("locks=%d unlocks=%d misuse=%d — the refusal path must give the lock back",
				locker.locks, locker.unlocks, locker.misuse)
		}
	})

	// NewListener is the only thing that sets conn, so a Listener built by hand
	// reaches here with nothing to read from.
	t.Run("nil conn", func(t *testing.T) {
		locker := &fakeLocker{}
		l := &Listener{configLocker: &sync.Mutex{}, listenLocker: locker}

		_, err := l.claimListen()

		if !errors.Is(err, ErrListenerConnIsNil) {
			t.Errorf("err = %v, want ErrListenerConnIsNil", err)
		}
		// Nothing was claimed, so a later claimListen must still succeed.
		if l.pauseChan != nil {
			t.Error("pauseChan was set despite the claim being refused")
		}
		if !locker.ok(1) {
			t.Errorf("locks=%d unlocks=%d misuse=%d", locker.locks, locker.unlocks, locker.misuse)
		}
	})

	// Pausing and starting again must not reset the assembly state, or the next
	// continuation frame finds no message open and validateFrame calls it a
	// protocol error.
	t.Run("keeps currentDataFrames", func(t *testing.T) {
		l := &Listener{
			configLocker:           &sync.Mutex{},
			conn:                   &fakeListenerConn{},
			currentDataFrames:      Frames{{Opcode: OpcodeText, PayloadData: []byte("he")}},
			currentDataFrameCount:  1,
			currentDataAccLength:   2,
			currentDataFrameOpcode: OpcodeText,
			listenLocker:           &fakeLocker{},
		}

		if _, err := l.claimListen(); err != nil {
			t.Fatalf("claimListen: %v", err)
		}

		if len(l.currentDataFrames) != 1 || l.currentDataFrameCount != 1 || l.currentDataAccLength != 2 {
			t.Errorf("currentDataFrames=%d currentDataFrameCount=%d currentDataAccLength=%d, want all untouched",
				len(l.currentDataFrames), l.currentDataFrameCount, l.currentDataAccLength)
		}
		if l.currentDataFrameOpcode != OpcodeText {
			t.Error("the message type should have survived the restart")
		}
	})
}

// fakeListenerConn scripts the frames listenFrames reads, and records what it
// was asked for.
type fakeListenerConn struct {
	frames             []*Frame
	next               int
	limits             []uint64
	deadlines          []time.Time
	getNextFrameErr    error
	setReadDeadlineErr error
}

func (conn *fakeListenerConn) GetNextFrame(maxByteLength uint64) (*Frame, error) {
	conn.limits = append(conn.limits, maxByteLength)
	if conn.next >= len(conn.frames) {
		return nil, conn.getNextFrameErr
	}
	frame := conn.frames[conn.next]
	conn.next++
	return frame, nil
}

func (conn *fakeListenerConn) SetReadDeadline(t time.Time) error {
	conn.deadlines = append(conn.deadlines, t)
	return conn.setReadDeadlineErr
}

/*
listenFrames is the read loop, so these drive it with every step stubbed: what
it reads, what it validates, what it routes, and when it gives up.

A conn whose script runs out returns getNextFrameErr, which is what ends a run
that is not paused or failed some other way.
*/
func TestListenerListenFrames(t *testing.T) {
	drained := errors.New("drained")

	// listenerFramesDI with every step succeeding, for a test to override one.
	workingDI := func() listenFramesDI {
		return listenFramesDI{
			timeNow:            time.Now,
			nextFrameByteLimit: func() uint64 { return 0 },
			validateFrame:      func(*Frame) error { return nil },
			routeFrame:         func(*Frame) error { return nil },
		}
	}

	// The pause is checked before the read, so a Listener paused between frames
	// never blocks on one more.
	t.Run("pauseChan closed", func(t *testing.T) {
		conn := &fakeListenerConn{getNextFrameErr: drained}
		l := &Listener{configLocker: &sync.Mutex{}, conn: conn}

		pauseChan := make(chan struct{})
		close(pauseChan)

		if err := l.listenFrames(pauseChan, workingDI()); err != nil {
			t.Errorf("err = %v, want nil", err)
		}
		if len(conn.limits) != 0 {
			t.Errorf("read %d times after being paused", len(conn.limits))
		}
	})

	t.Run("order", func(t *testing.T) {
		conn := &fakeListenerConn{
			frames:          []*Frame{{Opcode: OpcodeText}, {Opcode: OpcodeBinary}},
			getNextFrameErr: drained,
		}
		l := &Listener{configLocker: &sync.Mutex{}, conn: conn}

		var steps []string
		di := workingDI()
		di.validateFrame = func(*Frame) error { steps = append(steps, "validateFrame"); return nil }
		di.routeFrame = func(*Frame) error { steps = append(steps, "routeFrame"); return nil }

		if err := l.listenFrames(make(chan struct{}), di); !errors.Is(err, drained) {
			t.Fatalf("err = %v, want drained", err)
		}
		want := "validateFrame,routeFrame,validateFrame,routeFrame"
		if got := strings.Join(steps, ","); got != want {
			t.Errorf("steps = %s, want %s", got, want)
		}
	})

	// Whatever nextFrameByteLimit returns is what the frame is allowed to
	// declare, so it has to reach GetNextFrame unchanged.
	t.Run("nextFrameByteLimit", func(t *testing.T) {
		conn := &fakeListenerConn{frames: []*Frame{{}}, getNextFrameErr: drained}
		l := &Listener{configLocker: &sync.Mutex{}, conn: conn}

		di := workingDI()
		di.nextFrameByteLimit = func() uint64 { return 4242 }

		l.listenFrames(make(chan struct{}), di)
		for _, limit := range conn.limits {
			if limit != 4242 {
				t.Errorf("GetNextFrame got %d, want 4242", limit)
			}
		}
	})

	t.Run("FrameReadTimeout", func(t *testing.T) {
		fixed := time.Unix(1000, 0)
		conn := &fakeListenerConn{getNextFrameErr: drained}
		l := &Listener{configLocker: &sync.Mutex{}, conn: conn}
		l.config = ListenerConfig{FrameReadTimeout: 30 * time.Second}

		di := workingDI()
		di.timeNow = func() time.Time { return fixed }

		l.listenFrames(make(chan struct{}), di)
		if len(conn.deadlines) != 1 {
			t.Fatalf("SetReadDeadline called %d times, want 1", len(conn.deadlines))
		}
		if want := fixed.Add(30 * time.Second); !conn.deadlines[0].Equal(want) {
			t.Errorf("deadline = %v, want %v", conn.deadlines[0], want)
		}
	})

	// 0 means no timeout, and arming one anyway would clobber a deadline the
	// caller set on the conn themselves.
	t.Run("no FrameReadTimeout", func(t *testing.T) {
		conn := &fakeListenerConn{getNextFrameErr: drained}
		l := &Listener{configLocker: &sync.Mutex{}, conn: conn}

		l.listenFrames(make(chan struct{}), workingDI())
		if len(conn.deadlines) != 0 {
			t.Errorf("SetReadDeadline called %d times, want 0", len(conn.deadlines))
		}
	})

	// Every step can end the run, and the error has to arrive unchanged.
	t.Run("errors", func(t *testing.T) {
		want := errors.New("boom")
		tests := []struct {
			name  string
			setup func(*fakeListenerConn, *listenFramesDI)
		}{
			{"SetReadDeadline", func(conn *fakeListenerConn, _ *listenFramesDI) {
				conn.setReadDeadlineErr = want
			}},
			{"GetNextFrame", func(conn *fakeListenerConn, _ *listenFramesDI) {
				conn.getNextFrameErr = want
			}},
			{"validateFrame", func(_ *fakeListenerConn, di *listenFramesDI) {
				di.validateFrame = func(*Frame) error { return want }
			}},
			{"routeFrame", func(_ *fakeListenerConn, di *listenFramesDI) {
				di.routeFrame = func(*Frame) error { return want }
			}},
		}
		for _, testCase := range tests {
			t.Run(testCase.name, func(t *testing.T) {
				conn := &fakeListenerConn{frames: []*Frame{{}}, getNextFrameErr: drained}
				di := workingDI()
				testCase.setup(conn, &di)

				l := &Listener{configLocker: &sync.Mutex{}, conn: conn}
				l.config = ListenerConfig{FrameReadTimeout: time.Second}

				if err := l.listenFrames(make(chan struct{}), di); !errors.Is(err, want) {
					t.Errorf("err = %v, want %v", err, want)
				}
			})
		}
	})
}

/*
pauseListen is what releases a claim, so a missed Unlock deadlocks the next
caller and a missed clear leaves the Listener claimed forever. Neither shows up
as a wrong answer, which is what the counting Locker is for.
*/
func TestListenerPauseListen(t *testing.T) {
	t.Run("closes and clears pauseChan", func(t *testing.T) {
		pauseChan := make(chan struct{})
		locker := &fakeLocker{}
		l := &Listener{configLocker: &sync.Mutex{}, pauseChan: pauseChan, listenLocker: locker}

		var closed []chan struct{}
		l.pauseListen(pauseListenDI{
			closeChan: func(c chan struct{}) { closed = append(closed, c) },
		})

		if len(closed) != 1 || closed[0] != pauseChan {
			t.Errorf("closeChan called %d time(s) with the wrong channel", len(closed))
		}
		// Clearing is what releases the claim; leaving it set would make every
		// later Listen return ErrListenerIsListening.
		if l.pauseChan != nil {
			t.Error("pauseChan was not cleared")
		}
		if !locker.ok(1) {
			t.Errorf("locks=%d unlocks=%d misuse=%d", locker.locks, locker.unlocks, locker.misuse)
		}
	})

	// Closing an already closed channel panics, so a nil pauseChan has to be a
	// no-op — listen defers this on every path out, including ones where
	// PauseListen already ran.
	t.Run("nil pauseChan", func(t *testing.T) {
		locker := &fakeLocker{}
		l := &Listener{configLocker: &sync.Mutex{}, listenLocker: locker}

		closes := 0
		l.pauseListen(pauseListenDI{
			closeChan: func(chan struct{}) { closes++ },
		})

		if l.pauseChan != nil {
			t.Error("pauseChan should still be nil")
		}
		if closes != 0 {
			t.Errorf("closeChan called %d time(s) with nothing to close", closes)
		}
		if !locker.ok(1) {
			t.Errorf("locks=%d unlocks=%d misuse=%d — the no-op path must give the lock back",
				locker.locks, locker.unlocks, locker.misuse)
		}
	})

	t.Run("twice", func(t *testing.T) {
		l := &Listener{configLocker: &sync.Mutex{}, pauseChan: make(chan struct{}), listenLocker: &fakeLocker{}}
		closes := 0
		di := pauseListenDI{
			closeChan: func(c chan struct{}) { closes++; close(c) },
		}

		l.pauseListen(di)
		l.pauseListen(di) // a real second close would panic

		if closes != 1 {
			t.Errorf("closeChan called %d time(s), want 1", closes)
		}
	})
}
