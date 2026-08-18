package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/cplieger/health"
)

// --- Main ---

// main dispatches on the first argument: `health` runs the Docker probe,
// `scan` triggers one run via the daemon's socket and exits with that run's
// result (the external-trigger entry point), and anything else (including no
// argument) runs the long-lived process that owns all runs.
func main() {
	// CLI health probe for the Docker healthcheck (distroless has no
	// curl/wget). Checked before the logger is configured because RunProbe
	// calls os.Exit. Built-in mode arms a freshness deadline (probeOptions).
	if len(os.Args) > 1 && os.Args[1] == "health" {
		health.RunProbe(healthMarkerPath, probeOptions()...)
	}
	os.Exit(dispatch())
}

// dispatch selects the subcommand and returns the process exit code.
// Returning the code (rather than calling os.Exit here) keeps the routing
// testable and lets deferred cleanup in the daemon run before exit.
func dispatch() int {
	cmd := "daemon"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	switch cmd {
	case "daemon":
		// run/bootstrap already logs each failure once at the layer with the
		// most context, so exit non-zero here without a bare re-log.
		if err := run(context.Background()); err != nil {
			return 1
		}
		return 0
	case "scan":
		return runClient(socketPath)
	default:
		// An unrecognized subcommand is almost certainly a typo; fail
		// loudly instead of silently falling through to the daemon.
		setupLogger()
		slog.Error("unknown subcommand", "command", cmd, logKeyOutcome, "bad_subcommand", "valid", "scan, health")
		return 2
	}
}

// run is the composition root for the long-running container (the default
// no-arg command). It wires dependencies and dispatches on the configured
// mode: run-once performs a single direct run and exits, while the two
// long-running modes hand ownership of every run to the daemon (executor +
// trigger socket, plus the interval ticker in built-in mode). Returning an
// error exits non-zero.
func run(ctx context.Context) error {
	// Construct the marker before bootstrap so a startup bootstrap failure --
	// e.g. verifyCacheDir when /cache is read-only or full -- clears any stale
	// healthy marker left by a SIGKILLed prior run and honors the documented
	// "built-in mode begins unhealthy" / "unhealthy when /cache is full or
	// read-only" contract. NewMarker probes /tmp (healthMarkerPath), not
	// /cache, so it constructs fine before bootstrap. The daemon owns the
	// marker file for the container's lifetime and cleans up on exit.
	marker := health.NewMarker(healthMarkerPath)
	defer marker.Cleanup()

	cfg, err := bootstrap(ctx)
	if err != nil {
		marker.Set(false)
		return err
	}

	// Reclaim report temp files orphaned in /cache by a previous hard-killed
	// scan. Safe here: this process has no scan in flight at startup, and the
	// sweep takes the cross-container lock before touching anything.
	cleanStaleReports()

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch cfg.Mode {
	case modeOnce:
		return runOnce(ctx, marker, &cfg)
	case modeBuiltin, modeExternal:
		return runDaemon(ctx, &cfg, newHealthController(marker), defaultCommandRunner)
	default:
		panic(fmt.Sprintf("unhandled runMode: %d", int(cfg.Mode)))
	}
}

// bootstrap performs the shared startup prologue for every mode: it installs
// the logger, loads and validates config, and verifies the cache directory
// is writable. Each failure is logged exactly once at the layer with the
// most context — loadConfig logs the specific invalid setting at the leaf,
// and the cache-dir failure is logged here — so callers exit non-zero
// without re-logging.
func bootstrap(ctx context.Context) (config, error) {
	setupLogger()

	cfg, err := loadConfig()
	if err != nil {
		return config{}, err
	}

	if err := verifyCacheDir(ctx); err != nil {
		slog.Error("cache directory verification failed",
			"path", cacheDir, "uid", os.Getuid(), logKeyOutcome, "cache_error", "error", err)
		return config{}, err
	}

	return cfg, nil
}

// runOnce performs exactly one scan+action then returns, so the process
// exits afterward -- the one-shot mode selected by SCAN_INTERVAL=0. It
// runs the job directly (no daemon, no socket: there is nothing to trigger
// out-of-band in a process that exits after one run) and is the marker's
// single writer for its short life. A failed scan returns a non-nil error so
// the container exits non-zero (visible to an orchestrator running it as a
// batch job).
//
// run-once exits non-zero on every outcome that did NOT complete a clean
// scan, because here the exit code IS the batch job result. Three
// non-success cases, all distinct from the daemon modes which treat them as
// benign:
//   - exec failure / timeout: runFclonesJob returns a non-nil error (handled below).
//   - interrupt (SIGTERM/SIGINT mid-run): runFclonesJob treats a parent-context
//     cancellation as a clean stop and returns (ran=true, nil); for the one-shot, a
//     pod evicted / deadline-killed mid-scan must instead fail so the orchestrator
//     retries, so a cancelled context is converted to an error.
//   - lock-contention skip: another process held the /cache scan lock, so
//     runFclonesJob returned (ran=false, nil) WITHOUT scanning. The daemon modes
//     correctly no-op here, but a one-shot that performed no scan is not a
//     successful run, so it is reported as a non-zero SKIPPED outcome (and the
//     marker set unhealthy, since no successful scan recorded one) rather than a
//     silent exit-0 no-op the orchestrator would mark Complete.
func runOnce(ctx context.Context, marker *health.Marker, cfg *config) error {
	// Begins unhealthy: clear any stale health file from a previous run that
	// crashed before its defer ran. The single scan flips it on success.
	marker.Set(false)

	slog.Info("container started (run once)",
		"uid", os.Getuid(), "mode", cfg.Mode, "target", cfg.ScanPath, "action", cfg.Action,
		"phase_timeout", cfg.PhaseTimeout)

	ran, err := runFclonesJob(ctx, cfg, "once", defaultCommandRunner)
	if set, healthy := jobHealthSignal(ctx.Err(), ran, err); set {
		marker.Set(healthy)
	}
	switch outcome := classifyRunOnceOutcome(ran, err, ctx.Err()); outcome {
	case runOnceFailed:
		// Exec failure or timeout: already logged with full context; exit non-zero.
		return err
	case runOnceSkipped:
		// The scan lock was held by another process, so no scan or action ran.
		// jobHealthSignal writes no marker on a skip, so set it unhealthy here
		// so the exit code and the healthcheck agree (this run accomplished
		// nothing). Report SKIPPED as a non-zero outcome so a batch
		// orchestrator retries instead of recording a no-op as success.
		marker.Set(false)
		slog.Warn("run-once skipped: another process holds the scan lock; no scan ran",
			logKeyOutcome, "skipped", "lock", lockFile)
		return errors.New("run-once skipped: scan lock held by another process")
	case runOnceInterrupted:
		// Interrupted before the single run completed: report a non-zero exit so
		// a batch orchestrator treats the cut-short run as a failure, not a success.
		// The phase logged this as INFO outcome=shutdown (benign for a daemon); re-log
		// at WARN with the outcome tag so the batch failure is visible to a log-based
		// alert and not mistaken for a clean daemon shutdown. Reuses the existing
		// "shutdown" outcome value (as the skip path reuses "skipped"); WARN vs the
		// phase's INFO disambiguates.
		slog.Warn("run-once interrupted before completion; batch run failed",
			logKeyOutcome, "shutdown", "cause", context.Cause(ctx))
		return fmt.Errorf("run-once interrupted before completion: %w", context.Cause(ctx))
	case runOnceOK:
		slog.Info("run once complete", logKeyOutcome, "success")
		return nil
	default:
		panic(fmt.Sprintf("unhandled runOnceOutcome: %d", int(outcome)))
	}
}
