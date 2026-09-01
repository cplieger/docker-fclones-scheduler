package main

import (
	"log/slog"
	"os"
	"sync"

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

// healthMarker is the marker behaviour healthController depends on.
// *health.Marker satisfies it; tests inject a fake to observe writes.
type healthMarker interface {
	Set(healthy bool)
}

// healthController is the single writer of the health marker in the daemon
// modes. It enforces one invariant the bare marker cannot: once shutdown
// begins, health is monotonic toward unhealthy — a run finishing during
// drain can never flip the marker back to healthy.
type healthController struct {
	marker   healthMarker
	mu       sync.Mutex
	draining bool
}

// newHealthController returns a controller that writes through marker.
func newHealthController(marker healthMarker) *healthController {
	return &healthController{marker: marker}
}

// markInitial sets the pre-run state: unhealthy for the built-in scheduler
// (no run has completed yet, so the first successful one flips it) and
// healthy for the idle external-trigger container (nothing has failed). It
// is a no-op once draining.
func (h *healthController) markInitial(healthy bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.draining {
		return
	}
	h.marker.Set(healthy)
}

// apply writes one run's health value, unless shutdown has begun and that
// value is healthy (the drain latch stops a late success from masking
// shutdown). Callers gate on jobHealthSignal's set flag first.
func (h *healthController) apply(healthy bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.draining && healthy {
		return
	}
	h.marker.Set(healthy)
}

// beginDrain latches shutdown and marks unhealthy immediately, so observers
// see the draining signal before in-flight work finishes. After it, apply
// and markInitial can never restore healthy.
func (h *healthController) beginDrain() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.draining = true
	h.marker.Set(false)
}

// markUnhealthy writes an unconditional unhealthy marker for a failure that
// happens outside a run (a startup bootstrap failure). Unhealthy writes are
// always permitted — draining or not — so this takes the lock only to
// serialize with the other writers.
func (h *healthController) markUnhealthy() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.marker.Set(false)
}
