package main

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/cplieger/go/envx"
	"github.com/cplieger/go/health/v2"
	"github.com/cplieger/go/scheduler"
)

// healthMarkerPath is the file the daemon mirrors its verdict to; Docker's
// HEALTHCHECK re-invokes the binary with the `health` subcommand, which
// reads it.
var healthMarkerPath = health.MarkerPath("docker-fclones-scheduler")

// probeLease returns the lease the health subcommand arms, the one
// healthLease derives. An invalid env value disarms it (0) rather than
// risking a false-unhealthy restart loop.
func probeLease() time.Duration {
	quiet := slog.New(slog.DiscardHandler)
	s := scheduler.ParseInterval(os.Getenv("SCAN_INTERVAL"), defaultInterval,
		scheduler.WithZeroAsOnce(true), scheduler.WithName("SCAN_INTERVAL"),
		scheduler.WithIntervalLogger(quiet))
	if s.Mode != scheduler.ModeBuiltin {
		return 0
	}
	timeout, ok, err := envx.DurationStrict("SCAN_TIMEOUT")
	if err != nil {
		return 0
	}
	if !ok {
		timeout = defaultScanTimeout
	}
	return healthLease(modeBuiltin, s.Interval, timeout)
}

// healthLease is how old a healthy verdict may grow before the run loop counts as
// stuck: two intervals plus two worst-case runs, in built-in mode with a
// bounded SCAN_TIMEOUT. External and run-once modes, and an unbounded
// timeout, arm none (0).
func healthLease(mode runMode, interval, timeout time.Duration) time.Duration {
	if mode != modeBuiltin || timeout <= 0 {
		return 0
	}
	return health.Lease{Interval: interval, Cycles: 2, Timeout: timeout, Attempts: 2}.Duration()
}

// Reasons /healthz lists. Fixed sentences, because the route answers with no login.
const (
	reasonRunFailed = "the last run failed, no run has finished since the start, or the container is stopping"
	reasonStuck     = "no run has finished within two scan intervals plus two scan timeouts, so the scheduler may be stuck"
)

// newHealthz serves st's verdict as one check per fault, so each names itself.
func newHealthz(st *health.Status, cfg *config) http.Handler {
	return health.CheckHandler(
		health.Check{Signal: st, Reason: reasonRunFailed},
		health.Check{Signal: st.Fresh(healthLease(cfg.Mode, cfg.Interval, cfg.PhaseTimeout)), Reason: reasonStuck},
	)
}

// jobHealthSignal translates a finished job into a health decision. A
// cross-container lock skip (ran=false with no error) performed no scan, so
// it carries no health signal and the verdict keeps reflecting the last real
// run; everything else follows markerAction's interrupted-run carve-out.
func jobHealthSignal(ctxErr error, ran bool, runErr error) (set, healthy bool) {
	if !ran && runErr == nil {
		return false, false
	}
	return markerAction(ctxErr, runErr)
}
