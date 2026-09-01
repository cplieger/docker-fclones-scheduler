package main

import (
	"log/slog"
	"os"

	"github.com/cplieger/envx/v2"
	"github.com/cplieger/health"
	"github.com/cplieger/scheduler/v4"
)

// healthMarkerPath is where the health marker file lives; Docker's
// HEALTHCHECK re-invokes the binary with the `health` subcommand, which
// stats this path.
const healthMarkerPath = health.DefaultPath

// probeOptions returns the healthcheck probe's freshness policy. Built-in
// mode arms a max-age deadline (two intervals plus the worst-case run
// duration) so a wedged loop eventually probes unhealthy. External and
// run-once modes keep no deadline. An invalid env value disarms the
// deadline rather than risking a false-unhealthy restart loop.
func probeOptions() []health.ProbeOption {
	quiet := slog.New(slog.DiscardHandler)
	s := scheduler.ParseInterval(os.Getenv("SCAN_INTERVAL"), defaultInterval,
		scheduler.WithZeroAsOnce(true), scheduler.WithName("SCAN_INTERVAL"),
		scheduler.WithIntervalLogger(quiet))
	if s.Mode == scheduler.ModeExternal || s.Mode == scheduler.ModeOnce {
		return nil
	}
	timeout, ok, err := envx.DurationStrict("SCAN_TIMEOUT")
	if err != nil {
		return nil
	}
	if !ok {
		timeout = defaultScanTimeout
	}
	if timeout <= 0 {
		return nil
	}
	return []health.ProbeOption{health.WithMaxAge(2*s.Interval + 2*timeout)}
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
