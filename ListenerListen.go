package wlgows

import "time"

/*
Listen reads frames and routes them to the hooks, blocking until a read fails, a
frame breaks a rule, or PauseListen is called.

A pause returns whatever it was given, so nil means someone ended the run and
had nothing to report, and anything else is theirs rather than the protocol's —
a shutdown signal, a deadline your own timer kept, a rule this package does not
know about. Read errors and protocol errors come back the same way they always
did.

Every return leaves the connection open — see the type comment. A returned error
is a reason to close, never a sign that Listen already did.

Calling it again after a pause resumes where it left off: a fragmented message
half assembled when you paused is still open, so the configuration may be
replaced in between without losing frames. SetConfig does not need the pause —
it is safe under a running loop — but a pause is what makes the change land on
a message boundary rather than between two of its frames.
*/
func (l *Listener) Listen() error {
	framesDI := listenFramesDI{
		timeNow:            time.Now,
		nextFrameByteLimit: l.nextFrameByteLimit,
		validateFrame:      l.validateFrame,
		routeFrame:         l.routeFrame,
	}
	return l.listen(listenDI{
		claimListen: l.claimListen,
		pauseListen: l.PauseListen,
		listenFrames: func(pauseChan <-chan error) error {
			return l.listenFrames(pauseChan, framesDI)
		},
	})
}

// listenDI is the two halves listen puts together, so a test can watch it claim
// before it loops and release however the loop ends.
type listenDI struct {
	claimListen  func() (chan error, error)
	pauseListen  func(err error)
	listenFrames func(pauseChan <-chan error) error
}

func (l *Listener) listen(di listenDI) error {
	pauseChan, err := di.claimListen()
	if err != nil {
		return err
	}
	// Nothing to report from here: a run ending on its own carries its reason
	// back as listenFrames' return, and one ending on a pause has already had
	// the claim released by the pauser.
	defer di.pauseListen(nil)
	return di.listenFrames(pauseChan)
}

/*
claimListen takes the Listener for one run and hands back the channel that run
watches.

Two loops on one connection would each take an arbitrary subset of the frames,
so the second is refused. pauseChan doubles as the flag saying one is already
running.
*/
func (l *Listener) claimListen() (chan error, error) {
	l.listenLocker.Lock()
	defer l.listenLocker.Unlock()

	if l.pauseChan != nil {
		return nil, ErrListenerIsListening
	}
	// NewListener is the only thing that sets it, so nil means the Listener was
	// built by hand. Reading from it would panic on the first frame.
	if l.conn == nil {
		return nil, ErrListenerConnIsNil
	}
	l.pauseChan = make(chan error, 1) // one error, from whoever pauses first

	// The zero value is a nil slice, which appends and reads as empty just the
	// same — this only makes "never nil" true literally, so the rest never has
	// to ask. A restart leaves an open message alone.
	if l.currentDataFrames == nil {
		l.currentDataFrames = Frames{}
	}
	return l.pauseChan, nil
}

// listenFramesDI is every step the loop takes on a frame. The conn is not here
// because the Listener already carries it — a scripted conn is what feeds the
// loop its frames. timeNow is the only value that is not deterministic.
type listenFramesDI struct {
	timeNow            func() time.Time
	nextFrameByteLimit func() uint64
	validateFrame      func(f *Frame) error
	routeFrame         func(f *Frame) error
}

/*
listenFrames reads and routes until a read fails, a frame breaks a rule, or
pauseChan closes.

The pause is only noticed between frames, since the rest of the time this is
parked inside GetNextFrame.
*/
func (l *Listener) listenFrames(pauseChan <-chan error, di listenFramesDI) error {
	for {
		select {
		case err := <-pauseChan:
			return err
		default:
		}

		// Read per frame rather than once before the loop, which is what lets a
		// timeout set mid run land on the next frame instead of the next Listen.
		frameReadTimeout := l.GetConfig().FrameReadTimeout

		if frameReadTimeout > 0 {
			if err := l.conn.SetReadDeadline(di.timeNow().Add(frameReadTimeout)); err != nil {
				return err
			}
		}

		f, err := l.conn.GetNextFrame(di.nextFrameByteLimit())
		if err != nil {
			return err
		}
		if err := di.validateFrame(f); err != nil {
			return err
		}
		if err := di.routeFrame(f); err != nil {
			return err
		}
	}
}

/*
PauseListen ends the read loop, and Listen returns err. Pass nil to stop without
reporting anything. Safe from another goroutine, and safe to call when nothing
is listening, where it does nothing at all — including with the error.

Whoever pauses first is what Listen returns. A second pause finds the claim
already released and is dropped, error and all, rather than overwriting the
reason the run actually ended.

The pause is only noticed between frames, because the loop spends its time
parked inside GetNextFrame. On a silent peer this returns at once while the read
stays blocked; closing the connection is the only thing that unblocks it, and
that is yours to do.
*/
func (l *Listener) PauseListen(err error) {
	l.pauseListen(err, pauseListenDI{
		closeChan: func(pauseChan chan error) { close(pauseChan) },
	})
}

// pauseListenDI is what pausing reaches outside itself — the lock is no longer
// part of it, since listenLocker is on the Listener and a test substitutes it
// there.
type pauseListenDI struct {
	// close is a builtin, so it cannot be a field value on its own — this wraps
	// it. Substituting it is how a test sees the close happen without the
	// channel being closed for real.
	closeChan func(pauseChan chan error)
}

/*
pauseListen hands err to the run, closes the channel it is watching, and clears
it.

The send comes first and cannot block: the channel holds one error and the loop
may still be parked in a read, so a value waiting there is what it finds when it
next looks. Closing after that ends a run that was never given one.

Clearing is what releases the claim, so nil means nothing is running and a
second call has nothing to do — closing an already closed channel would panic.
*/
func (l *Listener) pauseListen(err error, di pauseListenDI) {
	l.listenLocker.Lock()
	defer l.listenLocker.Unlock()

	if l.pauseChan == nil {
		return
	}
	if err != nil {
		l.pauseChan <- err // buffered by one, and this is the only sender
	}
	di.closeChan(l.pauseChan)
	l.pauseChan = nil
}
