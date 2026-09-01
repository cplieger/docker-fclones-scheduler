package main

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/scheduler/v4/trigger"
)

// TestStartTickerDisabledInExternalMode pins that external mode runs no
// ticker: a ticker surviving into external mode would run unattended scans
// on a cadence the operator explicitly disabled, and with
// FCLONES_ACTION=remove those scans delete files.
//
// Runs in a synctest bubble because "no tick fired" is only probabilistic on
// a real clock: too short a sleep can't distinguish the invariant holding
// from a goroutine not yet scheduled.
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

		synctest.Sleep(20 * time.Millisecond)

		if n := len(d.queue.Jobs()); n != 0 {
			t.Errorf("%d scan requests submitted in external mode, want 0", n)
		}
	})
}
