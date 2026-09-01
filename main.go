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
// `scan` triggers one run via the daemon's socket, and anything else
// (including no argument) runs the long-lived process that owns all runs.
func main() {
	// Checked before the logger is configured because RunProbe calls os.Exit.
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
		if err := run(context.Background()); err != nil {
			return 1
		}
		return 0
	case "scan":
		return runClient(socketPath)
	default:
		setupLogger()
		slog.Error("unknown subcommand", "command", cmd, logKeyOutcome, "bad_subcommand", "valid", "scan, health")
		return 2
	}
}

// run is the composition root for the long-running container (the default
// no-arg command): wires dependencies and dispatches on the configured mode.
// Returning an error exits non-zero.
func run(ctx context.Context) error {
	// Constructed before bootstrap so a startup failure (e.g. /cache
	// read-only or full) still clears a stale healthy marker from a
	// SIGKILLed prior run. NewMarker probes /tmp, not /cache.
	marker := health.NewMarker(healthMarkerPath)
	defer marker.Cleanup()

	cfg, err := bootstrap(ctx)
	if err != nil {
		marker.Set(false)
		return err
	}

	// Safe here: this process has no scan in flight at startup, and the
	// sweep takes the cross-container lock before touching anything.
	cleanStaleReports()

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch cfg.Mode {
	case modeOnce:
		return runOnce(ctx, marker, &cfg)
	case modeBuiltin, modeExternal:
		return runDaemon(ctx, &cfg, health.NewLatch(marker), defaultCommandRunner)
	default:
		panic(fmt.Sprintf("unhandled runMode: %d", int(cfg.Mode)))
	}
}

// bootstrap performs the shared startup prologue for every mode: installs
// the logger, loads and validates config, and verifies the cache directory
// is writable. Each failure is logged exactly once at the layer with the
// most context.
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

// runOnce performs exactly one scan+action then returns (SCAN_INTERVAL=0).
// It runs the job directly (no daemon, no socket) and is the marker's single
// writer for its short life. Unlike the daemon modes, every non-success
// outcome here — exec failure, an interrupt mid-run, or a lock-contention
// skip — exits non-zero, because the exit code IS the batch job result.
func runOnce(ctx context.Context, marker *health.Marker, cfg *config) error {
	// Begins unhealthy: clears any stale health file from a previous run
	// that crashed before its defer ran.
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
		return err
	case runOnceSkipped:
		// jobHealthSignal writes no marker on a skip; set it here so the
		// exit code and the healthcheck agree.
		marker.Set(false)
		slog.Warn("run-once skipped: another process holds the scan lock; no scan ran",
			logKeyOutcome, "skipped", "lock", lockFile)
		return errors.New("run-once skipped: scan lock held by another process")
	case runOnceInterrupted:
		// The phase logged this at INFO (benign for a daemon); re-log at WARN
		// with the outcome tag so a batch failure isn't read as a clean stop.
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
