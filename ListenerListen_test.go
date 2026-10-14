package wlgows

import (
	"errors"
	"fmt"
	"slices"
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
		claimed := make(chan error, 1)
		var looped <-chan error

		err := (&Listener{configLocker: &sync.Mutex{}}).listen(listenDI{
			claimListen: func() (chan error, error) {
				steps = append(steps, "claimListen")
				return claimed, nil
			},
			listenFrames: func(pauseChan <-chan error) error {
				steps = append(steps, "listenFrames")
				looped = pauseChan
				return nil
			},
			pauseListen: func(error) { steps = append(steps, "pauseListen") },
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
			claimListen:  func() (chan error, error) { return nil, want },
			listenFrames: func(<-chan error) error { steps = append(steps, "listenFrames"); return nil },
			pauseListen:  func(error) { steps = append(steps, "pauseListen") },
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
			claimListen:  func() (chan error, error) { return make(chan error, 1), nil },
			listenFrames: func(<-chan error) error { return want },
			pauseListen:  func(error) { released = true },
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
rather than failing, so the recorded lock steps are what make it visible.
*/
func TestListenerClaimListen(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		var steps []string
		l := &Listener{configLocker: &sync.Mutex{}, conn: &fakeListenerConn{}}
		l.listenLocker = fakeFuncLocker{
			lock:   func() { steps = append(steps, "listen lock") },
			unlock: func() { steps = append(steps, fmt.Sprintf("listen unlock claimed=%v", l.pauseChan != nil)) },
		}

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
		// The claim is made before the unlock.
		if want := []string{"listen lock", "listen unlock claimed=true"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
	})

	// pauseChan being set is what says a run already has the Listener.
	t.Run("already listening", func(t *testing.T) {
		running := make(chan error, 1)
		var steps []string
		l := &Listener{configLocker: &sync.Mutex{}, pauseChan: running}
		l.listenLocker = fakeFuncLocker{
			lock:   func() { steps = append(steps, "listen lock") },
			unlock: func() { steps = append(steps, fmt.Sprintf("listen unlock claimed=%v", l.pauseChan != nil)) },
		}

		// conn is left nil on purpose: the refusal has to come first, so a
		// Listener already running is never reported as unbuilt.
		_, err := l.claimListen()

		if !errors.Is(err, ErrListenerIsListening) {
			t.Errorf("err = %v, want ErrListenerIsListening", err)
		}
		if l.pauseChan != running {
			t.Error("the running channel was replaced")
		}
		if want := []string{"listen lock", "listen unlock claimed=true"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q — the refusal path must give the lock back", steps, want)
		}
	})

	// NewListener is the only thing that sets conn, so a Listener built by hand
	// reaches here with nothing to read from.
	t.Run("nil conn", func(t *testing.T) {
		var steps []string
		l := &Listener{configLocker: &sync.Mutex{}}
		l.listenLocker = fakeFuncLocker{
			lock:   func() { steps = append(steps, "listen lock") },
			unlock: func() { steps = append(steps, fmt.Sprintf("listen unlock claimed=%v", l.pauseChan != nil)) },
		}

		_, err := l.claimListen()

		if !errors.Is(err, ErrListenerConnIsNil) {
			t.Errorf("err = %v, want ErrListenerConnIsNil", err)
		}
		// Nothing was claimed, so a later claimListen must still succeed.
		if l.pauseChan != nil {
			t.Error("pauseChan was set despite the claim being refused")
		}
		if want := []string{"listen lock", "listen unlock claimed=false"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
	})

	// Pausing and starting again must not reset the assembly state, or the next
	// continuation frame finds no message open and validateFrameHeader calls it a
	// protocol error.
	t.Run("keeps currentDataFrames", func(t *testing.T) {
		l := &Listener{
			configLocker:           &sync.Mutex{},
			conn:                   &fakeListenerConn{},
			currentDataFrames:      DataFrames{{Opcode: OpcodeText, PayloadData: []byte("he")}},
			currentDataFrameCount:  1,
			currentDataFramesSize:  2,
			currentDataFrameOpcode: OpcodeText,
			listenLocker:           fakeFuncLocker{lock: func() {}, unlock: func() {}},
		}

		if _, err := l.claimListen(); err != nil {
			t.Fatalf("claimListen: %v", err)
		}

		if len(l.currentDataFrames) != 1 || l.currentDataFrameCount != 1 || l.currentDataFramesSize != 2 {
			t.Errorf("currentDataFrames=%d currentDataFrameCount=%d currentDataFramesSize=%d, want all untouched",
				len(l.currentDataFrames), l.currentDataFrameCount, l.currentDataFramesSize)
		}
		if l.currentDataFrameOpcode != OpcodeText {
			t.Error("the message type should have survived the restart")
		}
	})
}

// fakeListenerConn runs whatever each test sets. A nil lockRead, unlockRead or
// setReadDeadline does nothing; set the read steps the test reaches.
type fakeListenerConn struct {
	lockRead           func()
	unlockRead         func()
	getNextFrameHeader func() (*Frame, error)
	readFramePayload   func(frame *Frame) error
	setReadDeadline    func(t time.Time) error
}

func (conn *fakeListenerConn) LockRead() {
	if conn.lockRead != nil {
		conn.lockRead()
	}
}

func (conn *fakeListenerConn) UnlockRead() {
	if conn.unlockRead != nil {
		conn.unlockRead()
	}
}

func (conn *fakeListenerConn) GetNextFrameHeader() (*Frame, error) {
	return conn.getNextFrameHeader()
}

func (conn *fakeListenerConn) ReadFramePayload(frame *Frame) error {
	return conn.readFramePayload(frame)
}

func (conn *fakeListenerConn) SetReadDeadline(t time.Time) error {
	if conn.setReadDeadline != nil {
		return conn.setReadDeadline(t)
	}
	return nil
}

/*
listenFrames is the read loop, so these drive it with every step stubbed: what
it reads, what it validates, what it routes, and when it gives up.

A conn whose frames run out returns drained, which is what ends a run that is
not paused or failed some other way.
*/
func TestListenerListenFrames(t *testing.T) {
	drained := errors.New("drained")

	// listenerFramesDI with every step succeeding, for a test to override one.
	workingDI := func() listenFramesDI {
		return listenFramesDI{
			timeNow:             time.Now,
			validateFrameHeader: func(*Frame) error { return nil },
			routeFrame:          func(*Frame) error { return nil },
		}
	}

	// The pause is checked before the read, so a Listener paused between frames
	// never blocks on one more.
	t.Run("pauseChan closed", func(t *testing.T) {
		conn := &fakeListenerConn{
			lockRead: func() { t.Error("read after being paused") },
		}
		listener := &Listener{configLocker: &sync.Mutex{}, conn: conn}

		pauseChan := make(chan error, 1)
		close(pauseChan)

		if err := listener.listenFrames(pauseChan, workingDI()); err != nil {
			t.Errorf("err = %v, want nil", err)
		}
	})

	/*
		A pause and a failed read are usually the same event — closing the
		connection is what wakes the read — so both come back joined rather than
		one hiding the other. The pause lands inside the read, the one moment it
		can meet the read error it caused.
	*/
	t.Run("a pause joins the read error it caused", func(t *testing.T) {
		readErr := errors.New("use of closed network connection")
		pauseErr := errors.New("shutting down")
		pauseChan := make(chan error, 1)
		conn := &fakeListenerConn{
			getNextFrameHeader: func() (*Frame, error) {
				pauseChan <- pauseErr // PauseListen, mirrored
				close(pauseChan)
				return nil, readErr
			},
		}
		listener := &Listener{configLocker: &sync.Mutex{}, conn: conn}

		err := listener.listenFrames(pauseChan, workingDI())

		if !errors.Is(err, pauseErr) {
			t.Errorf("err = %v, want the pause reason in it", err)
		}
		if !errors.Is(err, readErr) {
			t.Errorf("err = %v, want the read error in it too", err)
		}
	})

	// A frame validateFrameHeader refused is what Listen reports: a pause cannot
	// make it conforming, so the two are never joined.
	t.Run("a frame validateFrameHeader refused is not joined with a pause", func(t *testing.T) {
		pauseErr := errors.New("shutting down")
		pauseChan := make(chan error, 1)
		conn := &fakeListenerConn{
			getNextFrameHeader: func() (*Frame, error) {
				pauseChan <- pauseErr // PauseListen, mirrored
				close(pauseChan)
				return &Frame{Opcode: OpcodeText}, nil
			},
		}
		listener := &Listener{configLocker: &sync.Mutex{}, conn: conn}
		di := workingDI()
		di.validateFrameHeader = func(*Frame) error { return ErrReservedBitsSet }

		err := listener.listenFrames(pauseChan, di)

		if !errors.Is(err, ErrReservedBitsSet) {
			t.Errorf("err = %v, want %v", err, ErrReservedBitsSet)
		}
		if errors.Is(err, pauseErr) {
			t.Errorf("err = %v, want the pause left out", err)
		}
	})

	// Join drops nils, so a pause with nothing to report leaves the socket's own
	// error rather than turning a dead connection into a clean stop.
	t.Run("a nil pause leaves the read error alone", func(t *testing.T) {
		readErr := errors.New("connection reset")
		pauseChan := make(chan error, 1)
		conn := &fakeListenerConn{
			getNextFrameHeader: func() (*Frame, error) {
				close(pauseChan) // PauseListen(nil)
				return nil, readErr
			},
		}
		listener := &Listener{configLocker: &sync.Mutex{}, conn: conn}

		if err := listener.listenFrames(pauseChan, workingDI()); !errors.Is(err, readErr) {
			t.Errorf("err = %v, want %v", err, readErr)
		}
	})

	// A pause carries the pauser's reason, which is what the run returns instead
	// of nil — the whole point of the channel holding an error.
	t.Run("pauseChan carrying an error", func(t *testing.T) {
		want := errors.New("shutting down")
		conn := &fakeListenerConn{
			lockRead: func() { t.Error("read after being paused") },
		}
		listener := &Listener{configLocker: &sync.Mutex{}, conn: conn}

		pauseChan := make(chan error, 1)
		pauseChan <- want
		close(pauseChan)

		if err := listener.listenFrames(pauseChan, workingDI()); !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
	})

	t.Run("order", func(t *testing.T) {
		frames := []*Frame{{Opcode: OpcodeText}, {Opcode: OpcodeBinary}}
		conn := &fakeListenerConn{
			getNextFrameHeader: func() (*Frame, error) {
				if len(frames) == 0 {
					return nil, drained
				}
				frame := frames[0]
				frames = frames[1:]
				return frame, nil
			},
			readFramePayload: func(*Frame) error { return nil },
		}
		listener := &Listener{configLocker: &sync.Mutex{}, conn: conn}

		var steps []string
		di := workingDI()
		di.validateFrameHeader = func(*Frame) error { steps = append(steps, "validateFrameHeader"); return nil }
		di.routeFrame = func(*Frame) error { steps = append(steps, "routeFrame"); return nil }

		if err := listener.listenFrames(make(chan error, 1), di); !errors.Is(err, drained) {
			t.Fatalf("err = %v, want drained", err)
		}
		want := "validateFrameHeader,routeFrame,validateFrameHeader,routeFrame"
		if got := strings.Join(steps, ","); got != want {
			t.Errorf("steps = %s, want %s", got, want)
		}
	})

	/*
		One read lock covers the whole frame, header and payload, so no other
		reader takes bytes between the two steps. The header is judged before
		the payload is read, and the lock is released before routeFrame, so no
		hook runs under it.
	*/
	t.Run("one frame read whole", func(t *testing.T) {
		frame := &Frame{Opcode: OpcodeText}
		var steps []string
		headerReads := 0
		conn := &fakeListenerConn{
			lockRead:   func() { steps = append(steps, "lock") },
			unlockRead: func() { steps = append(steps, "unlock") },
			getNextFrameHeader: func() (*Frame, error) {
				steps = append(steps, "header")
				headerReads++
				if headerReads > 1 { // one frame, then the stream ends
					return nil, drained
				}
				return frame, nil
			},
			readFramePayload: func(payloadFrame *Frame) error {
				if payloadFrame != frame {
					t.Error("the payload was read into another frame")
				}
				steps = append(steps, "payload")
				return nil
			},
		}
		listener := &Listener{configLocker: &sync.Mutex{}, conn: conn}
		di := workingDI()
		di.validateFrameHeader = func(*Frame) error { steps = append(steps, "validate"); return nil }
		di.routeFrame = func(*Frame) error { steps = append(steps, "route"); return nil }

		listener.listenFrames(make(chan error, 1), di)
		want := "lock,header,validate,payload,unlock,route,lock,header,unlock"
		if got := strings.Join(steps, ","); got != want {
			t.Errorf("steps = %s, want %s", got, want)
		}
	})

	// A frame refused by its header is never allocated, and the lock is still
	// released.
	t.Run("a frame validateFrameHeader refuses has no payload read", func(t *testing.T) {
		var steps []string
		conn := &fakeListenerConn{
			lockRead:           func() { steps = append(steps, "lock") },
			unlockRead:         func() { steps = append(steps, "unlock") },
			getNextFrameHeader: func() (*Frame, error) { return &Frame{Opcode: OpcodeText}, nil },
			readFramePayload: func(*Frame) error {
				t.Error("the payload of a refused frame was read")
				return nil
			},
		}
		listener := &Listener{configLocker: &sync.Mutex{}, conn: conn}

		di := workingDI()
		di.validateFrameHeader = func(*Frame) error { return ErrReservedBitsSet }

		if err := listener.listenFrames(make(chan error, 1), di); !errors.Is(err, ErrReservedBitsSet) {
			t.Errorf("err = %v, want %v", err, ErrReservedBitsSet)
		}
		if got := strings.Join(steps, ","); got != "lock,unlock" {
			t.Errorf("steps = %s, want lock,unlock", got)
		}
	})

	// A failed header read still releases the lock, and reads no payload.
	t.Run("a failed header read releases the lock", func(t *testing.T) {
		var steps []string
		conn := &fakeListenerConn{
			lockRead:           func() { steps = append(steps, "lock") },
			unlockRead:         func() { steps = append(steps, "unlock") },
			getNextFrameHeader: func() (*Frame, error) { return nil, drained },
			readFramePayload: func(*Frame) error {
				t.Error("a payload was read after the header read failed")
				return nil
			},
		}
		listener := &Listener{configLocker: &sync.Mutex{}, conn: conn}

		if err := listener.listenFrames(make(chan error, 1), workingDI()); !errors.Is(err, drained) {
			t.Errorf("err = %v, want drained", err)
		}
		if got := strings.Join(steps, ","); got != "lock,unlock" {
			t.Errorf("steps = %s, want lock,unlock", got)
		}
	})

	// A failed payload read still releases the lock.
	t.Run("a failed payload read releases the lock", func(t *testing.T) {
		var steps []string
		conn := &fakeListenerConn{
			lockRead:           func() { steps = append(steps, "lock") },
			unlockRead:         func() { steps = append(steps, "unlock") },
			getNextFrameHeader: func() (*Frame, error) { return &Frame{Opcode: OpcodeText}, nil },
			readFramePayload:   func(*Frame) error { return drained },
		}
		listener := &Listener{configLocker: &sync.Mutex{}, conn: conn}

		if err := listener.listenFrames(make(chan error, 1), workingDI()); !errors.Is(err, drained) {
			t.Errorf("err = %v, want drained", err)
		}
		if got := strings.Join(steps, ","); got != "lock,unlock" {
			t.Errorf("steps = %s, want lock,unlock", got)
		}
	})

	t.Run("FrameReadTimeout", func(t *testing.T) {
		fixed := time.Unix(1000, 0)
		var deadlines []time.Time
		conn := &fakeListenerConn{
			setReadDeadline:    func(deadline time.Time) error { deadlines = append(deadlines, deadline); return nil },
			getNextFrameHeader: func() (*Frame, error) { return nil, drained },
		}
		listener := &Listener{configLocker: &sync.Mutex{}, conn: conn}
		listener.config = ListenerConfig{FrameReadTimeout: 30 * time.Second}

		di := workingDI()
		di.timeNow = func() time.Time { return fixed }

		listener.listenFrames(make(chan error, 1), di)
		if len(deadlines) != 1 {
			t.Fatalf("SetReadDeadline called %d times, want 1", len(deadlines))
		}
		if want := fixed.Add(30 * time.Second); !deadlines[0].Equal(want) {
			t.Errorf("deadline = %v, want %v", deadlines[0], want)
		}
	})

	// 0 means no timeout, and arming one anyway would clobber a deadline the
	// caller set on the conn themselves.
	t.Run("no FrameReadTimeout", func(t *testing.T) {
		conn := &fakeListenerConn{
			setReadDeadline: func(time.Time) error {
				t.Error("SetReadDeadline called with no FrameReadTimeout")
				return nil
			},
			getNextFrameHeader: func() (*Frame, error) { return nil, drained },
		}
		listener := &Listener{configLocker: &sync.Mutex{}, conn: conn}

		listener.listenFrames(make(chan error, 1), workingDI())
	})

	// Every step can end the run, and the error has to arrive unchanged.
	t.Run("errors", func(t *testing.T) {
		want := errors.New("boom")
		tests := []struct {
			name  string
			setup func(*fakeListenerConn, *listenFramesDI)
		}{
			{"SetReadDeadline", func(conn *fakeListenerConn, _ *listenFramesDI) {
				conn.setReadDeadline = func(time.Time) error { return want }
			}},
			{"GetNextFrameHeader", func(conn *fakeListenerConn, _ *listenFramesDI) {
				conn.getNextFrameHeader = func() (*Frame, error) { return nil, want }
			}},
			{"ReadFramePayload", func(conn *fakeListenerConn, _ *listenFramesDI) {
				conn.readFramePayload = func(*Frame) error { return want }
			}},
			{"validateFrameHeader", func(_ *fakeListenerConn, di *listenFramesDI) {
				di.validateFrameHeader = func(*Frame) error { return want }
			}},
			{"routeFrame", func(_ *fakeListenerConn, di *listenFramesDI) {
				di.routeFrame = func(*Frame) error { return want }
			}},
		}
		for _, testCase := range tests {
			t.Run(testCase.name, func(t *testing.T) {
				conn := &fakeListenerConn{
					getNextFrameHeader: func() (*Frame, error) { return &Frame{}, nil },
					readFramePayload:   func(*Frame) error { return nil },
				}
				di := workingDI()
				testCase.setup(conn, &di)

				listener := &Listener{configLocker: &sync.Mutex{}, conn: conn}
				listener.config = ListenerConfig{FrameReadTimeout: time.Second}

				if err := listener.listenFrames(make(chan error, 1), di); !errors.Is(err, want) {
					t.Errorf("err = %v, want %v", err, want)
				}
			})
		}
	})
}

/*
pauseListen is what releases a claim, so a missed Unlock deadlocks the next
caller and a missed clear leaves the Listener claimed forever. Neither shows up
as a wrong answer, which is what the recorded lock steps are for.
*/
func TestListenerPauseListen(t *testing.T) {
	t.Run("hands over the error, closes and clears pauseChan", func(t *testing.T) {
		want := errors.New("shutting down")
		pauseChan := make(chan error, 1)
		var steps []string
		l := &Listener{configLocker: &sync.Mutex{}, pauseChan: pauseChan}
		l.listenLocker = fakeFuncLocker{
			lock:   func() { steps = append(steps, "listen lock") },
			unlock: func() { steps = append(steps, fmt.Sprintf("listen unlock claimed=%v", l.pauseChan != nil)) },
		}

		var closed []chan error
		l.pauseListen(want, pauseListenDI{
			closeChan: func(c chan error) {
				steps = append(steps, "close")
				closed = append(closed, c)
			},
		})

		// The send comes before the close, so a run still parked in a read finds
		// the error waiting rather than only a closed channel.
		select {
		case got := <-pauseChan:
			if !errors.Is(got, want) {
				t.Errorf("the run was handed %v, want %v", got, want)
			}
		default:
			t.Error("nothing was handed to the run")
		}
		if len(closed) != 1 || closed[0] != pauseChan {
			t.Errorf("closeChan called %d time(s) with the wrong channel", len(closed))
		}
		// Clearing is what releases the claim; leaving it set would make every
		// later Listen return ErrListenerIsListening.
		if l.pauseChan != nil {
			t.Error("pauseChan was not cleared")
		}
		// The close and the release both happen before the unlock.
		if want := []string{"listen lock", "close", "listen unlock claimed=false"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
	})

	// nil is a stop with nothing to report, so the run reads the closed channel
	// and returns nil — sending would leave a value that says the same thing.
	t.Run("nil error sends nothing", func(t *testing.T) {
		pauseChan := make(chan error, 1)
		l := &Listener{
			configLocker: &sync.Mutex{},
			pauseChan:    pauseChan,
			listenLocker: fakeFuncLocker{lock: func() {}, unlock: func() {}},
		}

		l.pauseListen(nil, pauseListenDI{closeChan: func(chan error) {}})

		if len(pauseChan) != 0 {
			t.Errorf("%d value(s) waiting, want none", len(pauseChan))
		}
	})

	// Closing an already closed channel panics, so a nil pauseChan has to be a
	// no-op — listen defers this on every path out, including ones where
	// PauseListen already ran.
	t.Run("nil pauseChan", func(t *testing.T) {
		var steps []string
		l := &Listener{configLocker: &sync.Mutex{}}
		l.listenLocker = fakeFuncLocker{
			lock:   func() { steps = append(steps, "listen lock") },
			unlock: func() { steps = append(steps, fmt.Sprintf("listen unlock claimed=%v", l.pauseChan != nil)) },
		}

		closes := 0
		l.pauseListen(errors.New("dropped"), pauseListenDI{
			closeChan: func(chan error) { closes++ },
		})

		if l.pauseChan != nil {
			t.Error("pauseChan should still be nil")
		}
		if closes != 0 {
			t.Errorf("closeChan called %d time(s) with nothing to close", closes)
		}
		if want := []string{"listen lock", "listen unlock claimed=false"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q — the no-op path must give the lock back", steps, want)
		}
	})

	// Whoever pauses first is what the run returns. The second finds the claim
	// released, so its error goes nowhere rather than replacing the first.
	t.Run("twice", func(t *testing.T) {
		first := errors.New("first")
		pauseChan := make(chan error, 1)
		l := &Listener{
			configLocker: &sync.Mutex{},
			pauseChan:    pauseChan,
			listenLocker: fakeFuncLocker{lock: func() {}, unlock: func() {}},
		}
		closes := 0
		di := pauseListenDI{
			closeChan: func(c chan error) { closes++; close(c) },
		}

		l.pauseListen(first, di)
		l.pauseListen(errors.New("second"), di) // a real second close would panic

		if closes != 1 {
			t.Errorf("closeChan called %d time(s), want 1", closes)
		}
		if got := <-pauseChan; !errors.Is(got, first) {
			t.Errorf("the run was handed %v, want the first pauser's error", got)
		}
	})
}
