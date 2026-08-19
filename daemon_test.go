package main

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/scheduler/v4/trigger"
)

// TestStartTickerDisabledInExternalMode pins that external mode runs no
// ticker: startTicker returns an already-closed channel and submits nothing.
// The invariant is a safety one, not a tidiness one -- a ticker that survived
// into external mode would run unattended scans on a cadence the operator
// explicitly disabled, and with FCLONES_ACTION=remove those scans delete
// files.
//
// It runs in a synctest bubble because the negative half ("no tick fired") is
// only probabilistic on a real clock: a sleep long enough to be convincing is
// a slow test, and a sleep short enough to be fast cannot distinguish the
// invariant holding from a goroutine that simply had not been scheduled yet.
// Everything in the bubble is in-memory -- a buffered queue and a startTicker
// that starts no goroutine at all in this mode -- so the fake clock can always
// advance and synctest.Sleep settles the bubble in one call.
func TestStartTickerDisabledInExternalMode(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		d := &daemon{queue: trigger.NewQueue[struct{}](queueCapacity)}

		done := startTicker(t.Context(), d, time.Millisecond, false)

		select {
		case <-done:
		default:
			t.Fatal("startTicker(enabled=false) channel not already closed, want a closed channel so runDaemon's shutdown wait resolves immediately")
		}

		// Advance twenty virtual intervals and let the bubble settle. A
		// regression that started the loop submits its startup tick and then
		// parks in tick's <-j.Result() wait (no executor runs here), so it is
		// caught twice over: the queue length below is non-zero, and the
		// bubble reports the parked goroutine when it cannot reach a settled
		// state.
		synctest.Sleep(20 * time.Millisecond)

		if n := len(d.queue.Jobs()); n != 0 {
			t.Errorf("%d scan requests submitted in external mode, want 0", n)
		}
	})
}
