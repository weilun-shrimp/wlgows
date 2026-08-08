package wlgows

import "time"

/*
Loop calls trigger every interval until it is told to stop, blocking until then.

The stop is the work's own to decide: trigger is handed a channel and sends into
it once when the loop should end. That is checked the moment trigger returns, so
a loop that stops does not sit through another interval first.

	wlgows.Loop(func(stop chan<- struct{}) {
		if time.Since(lastPong) > deadline {
			stop <- struct{}{} // the peer is gone
			return
		}
		conn.SendPing(nil)
	}, 30*time.Second)

The interval is between two triggers, not around one, so a slow trigger delays
the next call rather than overlapping it. Anything that wants to end the loop
from outside gives trigger something to read — a channel of its own, a flag —
and trigger signals when it sees it. That is noticed on the next tick.

Send into the channel once. It holds one value, so a second send blocks forever.
*/
func Loop(trigger func(stop_signal chan<- struct{}), interval time.Duration) {
	stop_signal := make(chan struct{}, 1)
	for {
		select {
		case <-stop_signal:
			return
		default:
		}

		trigger(stop_signal)

		select {
		case <-stop_signal:
			return
		default:
		}

		time.Sleep(interval)
	}
}
