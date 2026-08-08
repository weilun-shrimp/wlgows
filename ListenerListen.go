package wlgows

import (
	"sync"
	"time"
)

/*
Listen reads frames and routes them to the hooks, blocking until a read fails, a
frame breaks a rule, or PauseListen is called.

Every return leaves the connection open — see the type comment. A returned error
is a reason to close, never a sign that Listen already did.

Calling it again after a pause resumes where it left off: a fragmented message
half assembled when you paused is still open, so SetConfig may be called in
between without losing frames.
*/
func (l *Listener) Listen() error {
	claimDI := claimListenDI{locker: &l.mu}
	framesDI := listenFramesDI{
		timeNow:            time.Now,
		nextFrameByteLimit: l.nextFrameByteLimit,
		validateFrame:      l.validateFrame,
		routeFrame:         l.routeFrame,
	}
	return l.listen(listenDI{
		claimListen: func() (chan struct{}, error) {
			return l.claimListen(claimDI)
		},
		pauseListen: l.PauseListen,
		listenFrames: func(pauseChan <-chan struct{}) error {
			return l.listenFrames(pauseChan, framesDI)
		},
	})
}

// listenDI is the two halves listen puts together, so a test can watch it claim
// before it loops and release however the loop ends.
type listenDI struct {
	claimListen  func() (chan struct{}, error)
	pauseListen  func()
	listenFrames func(pauseChan <-chan struct{}) error
}

func (l *Listener) listen(di listenDI) error {
	pauseChan, err := di.claimListen()
	if err != nil {
		return err
	}
	defer di.pauseListen()
	return di.listenFrames(pauseChan)
}

// claimListenDI is what claiming reaches outside itself. Taking the lock and
// releasing it leaves no trace of its own, so a counting Locker is how a test
// sees it happened, and happened once on every path out.
type claimListenDI struct {
	locker sync.Locker
}

/*
claimListen takes the Listener for one run and hands back the channel that run
watches.

Two loops on one connection would each take an arbitrary subset of the frames,
so the second is refused. pauseChan doubles as the flag saying one is already
running.
*/
func (l *Listener) claimListen(di claimListenDI) (chan struct{}, error) {
	di.locker.Lock()
	defer di.locker.Unlock()

	if l.pauseChan != nil {
		return nil, ErrListenerIsListening
	}
	if err := l.config.validate(); err != nil {
		return nil, err
	}
	l.pauseChan = make(chan struct{})

	// The zero value is a nil slice, which appends and reads as empty just the
	// same — this only makes "never nil" true literally, so the rest never has
	// to ask. A restart leaves an open message alone.
	if l.currentDataFrames == nil {
		l.currentDataFrames = Frames{}
	}
	return l.pauseChan, nil
}

// listenFramesDI is every step the loop takes on a frame. Conn is not here
// because ListenerConfig already carries it — a scripted conn is what feeds the
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
func (l *Listener) listenFrames(pauseChan <-chan struct{}, di listenFramesDI) error {
	for {
		select {
		case <-pauseChan:
			return nil
		default:
		}

		if l.config.FrameReadTimeout > 0 {
			if err := l.config.Conn.SetReadDeadline(di.timeNow().Add(l.config.FrameReadTimeout)); err != nil {
				return err
			}
		}

		f, err := l.config.Conn.GetNextFrame(di.nextFrameByteLimit())
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
PauseListen ends the read loop. Safe from another goroutine, and safe to call
when nothing is listening.

The pause is only noticed between frames, because the loop spends its time
parked inside GetNextFrame. On a silent peer this returns at once while the read
stays blocked; closing the connection is the only thing that unblocks it, and
that is yours to do.
*/
func (l *Listener) PauseListen() {
	l.pauseListen(pauseListenDI{
		locker:    &l.mu,
		closeChan: func(pauseChan chan struct{}) { close(pauseChan) },
	})
}

// pauseListenDI is what pausing reaches outside itself. Taking the lock and
// releasing it leaves no trace of its own, so a counting Locker is how a test
// sees it happened, and happened once.
type pauseListenDI struct {
	locker sync.Locker
	// close is a builtin, so it cannot be a field value on its own — this wraps
	// it. Substituting it is how a test sees the close happen without the
	// channel being closed for real.
	closeChan func(pauseChan chan struct{})
}

/*
pauseListen closes the channel the run is watching and clears it.

Clearing is what releases the claim, so nil means nothing is running and a
second call has nothing to do — closing an already closed channel would panic.
*/
func (l *Listener) pauseListen(di pauseListenDI) {
	di.locker.Lock()
	defer di.locker.Unlock()

	if l.pauseChan == nil {
		return
	}
	di.closeChan(l.pauseChan)
	l.pauseChan = nil
}
