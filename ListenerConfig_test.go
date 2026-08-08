package wlgows

import (
	"errors"
	"testing"
)

func TestListenerConfigValidate(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		config := ListenerConfig{Conn: &fakeListenerConn{}}
		if err := config.validate(); err != nil {
			t.Errorf("validate() = %v, want nil", err)
		}
	})

	// Conn is the only thing a read loop cannot do without. Everything else has
	// a working zero value: no timeout, no limit, and nil hooks that drop.
	t.Run("nil Conn", func(t *testing.T) {
		if err := (ListenerConfig{}).validate(); !errors.Is(err, ErrListenerConnIsNil) {
			t.Errorf("validate() = %v, want ErrListenerConnIsNil", err)
		}
	})
}

/*
setConfig is the only writer of Listener.config, and that is what lets the read
loop read it without a lock. So the two refusals matter as much as the write:
config must be unchanged after either.
*/
func TestListenerSetConfig(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		conn := &fakeListenerConn{}
		l := &Listener{}
		locker := &fakeLocker{}

		err := l.setConfig(
			ListenerConfig{Conn: conn, MaxMsgPayloadByteLen: 1000},
			setConfigDI{locker: locker},
		)

		if err != nil {
			t.Fatalf("setConfig: %v", err)
		}
		if l.config.Conn != conn || l.config.MaxMsgPayloadByteLen != 1000 {
			t.Error("config was not stored")
		}
		if !locker.ok(1) {
			t.Errorf("locks=%d unlocks=%d misuse=%d", locker.locks, locker.unlocks, locker.misuse)
		}
	})

	t.Run("invalid config", func(t *testing.T) {
		l := &Listener{}
		l.config = ListenerConfig{Conn: &fakeListenerConn{}, MaxMsgPayloadByteLen: 1000}
		locker := &fakeLocker{}

		err := l.setConfig(ListenerConfig{}, setConfigDI{locker: locker})

		if !errors.Is(err, ErrListenerConnIsNil) {
			t.Errorf("err = %v, want ErrListenerConnIsNil", err)
		}
		if l.config.MaxMsgPayloadByteLen != 1000 {
			t.Error("a refused config was stored anyway")
		}
		// validate runs first, so a config that could never run never makes a
		// running loop wait for the lock.
		if locker.locks != 0 {
			t.Errorf("locks = %d, want 0", locker.locks)
		}
	})

	// pauseChan being set is what says a run holds the Listener. Replacing the
	// config under it would change what the loop reads mid run.
	t.Run("already listening", func(t *testing.T) {
		l := &Listener{pauseChan: make(chan struct{})}
		l.config = ListenerConfig{Conn: &fakeListenerConn{}, MaxMsgPayloadByteLen: 1000}
		locker := &fakeLocker{}

		err := l.setConfig(
			ListenerConfig{Conn: &fakeListenerConn{}, MaxMsgPayloadByteLen: 2000},
			setConfigDI{locker: locker},
		)

		if !errors.Is(err, ErrListenerIsListening) {
			t.Errorf("err = %v, want ErrListenerIsListening", err)
		}
		if l.config.MaxMsgPayloadByteLen != 1000 {
			t.Error("the running config was replaced")
		}
		if !locker.ok(1) {
			t.Errorf("locks=%d unlocks=%d misuse=%d — the refusal path must give the lock back",
				locker.locks, locker.unlocks, locker.misuse)
		}
	})
}
