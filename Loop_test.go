package wlgows

import (
	"testing"
	"time"
)

/*
Loop runs trigger until the work says stop. The count proves it loops and that
the stop is obeyed; the clock proves the stop lands at once.

That second one is the ordering: the check has to sit between trigger and the
sleep. Move it after the sleep and everything still passes except the elapsed
time, which grows by a whole interval — a loop sitting on a connection it has
already been told is finished.
*/
func TestLoop(t *testing.T) {
	const interval = 30 * time.Millisecond
	calls := 0

	start := time.Now()
	Loop(func(stop chan<- struct{}) {
		calls++
		if calls == 3 {
			stop <- struct{}{}
		}
	}, interval)
	elapsed := time.Since(start)

	if calls != 3 {
		t.Errorf("trigger ran %d times, want 3", calls)
	}
	// Two sleeps, after the first and second calls. The third asks to stop, so
	// the loop returns without sleeping a third time.
	if elapsed >= 3*interval {
		t.Errorf("took %v, want under %v — a stop must not sit through another interval",
			elapsed, 3*interval)
	}
}
