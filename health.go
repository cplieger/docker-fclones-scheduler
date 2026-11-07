package main

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/cplieger/go/envx"
	"github.com/cplieger/go/health"
	"github.com/cplieger/go/scheduler"
)

// healthMarkerPath is where the health marker file lives; Docker's
// HEALTHCHECK re-invokes the binary with the `health` subcommand, which
// stats this path.
const healthMarkerPath = health.DefaultPath

// probeOptions returns the healthcheck probe's freshness policy, the lease
// healthLease derives. An invalid env value disarms the deadline rather than
// risking a false-unhealthy restart loop.
func probeOptions() []health.ProbeOption {
	quiet := slog.New(slog.DiscardHandler)
	s := scheduler.ParseInterval(os.Getenv("SCAN_INTERVAL"), defaultInterval,
		scheduler.WithZeroAsOnce(true), scheduler.WithName("SCAN_INTERVAL"),
		scheduler.WithIntervalLogger(quiet))
	if s.Mode != scheduler.ModeBuiltin {
		return nil
	}
	timeout, ok, err := envx.DurationStrict("SCAN_TIMEOUT")
	if err != nil {
		return nil
	}
	if !ok {
		timeout = defaultScanTimeout
	}
	lease := healthLease(modeBuiltin, s.Interval, timeout)
	if lease <= 0 {
		return nil
	}
	return []health.ProbeOption{health.WithMaxAge(lease)}
}

// healthLease is how old the marker may grow before the run loop counts as
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

// newHealthz serves the verdict the health subcommand reads from marker while
// the marker directory is writable, as one check per fault so each names itself.
func newHealthz(marker *health.Marker, cfg *config) http.Handler {
	return health.CheckHandler(
		health.Check{Signal: marker, Reason: reasonRunFailed},
		health.Check{Signal: notStale{marker: marker, lease: healthLease(cfg.Mode, cfg.Interval, cfg.PhaseTimeout)}, Reason: reasonStuck},
	)
}

// notStale fails a marker that reads healthy but is past the lease, so an
// absent marker reports reasonRunFailed alone and not reasonStuck too.
type notStale struct {
	marker *health.Marker
	lease  time.Duration
}

func (s notStale) Healthy() bool {
	return s.marker.Within(s.lease).Healthy() || !s.marker.Healthy()
}

// jobHealthSignal translates a finished job into a marker decision. A
// cross-container lock skip (ran=false with no error) performed no scan, so
// it carries no health signal and the marker keeps reflecting the last real
// run; everything else follows markerAction's interrupted-run carve-out.
func jobHealthSignal(ctxErr error, ran bool, runErr error) (set, healthy bool) {
	if !ran && runErr == nil {
		return false, false
	}
	return markerAction(ctxErr, runErr)
}
