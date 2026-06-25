package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
	// Embed the IANA tz database so TZ (e.g. the default Europe/Paris) is
	// honored. The distroless static base ships no /usr/share/zoneinfo, so
	// without this time.Local silently falls back to UTC and timestamps
	// ignore TZ.
	_ "time/tzdata"

	"github.com/cplieger/health"
)

// --- Main ---

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "health":
			// CLI health probe for Docker healthcheck (distroless has no
			// curl/wget). Checks for a marker file instead of making an
			// HTTP request — no port needed. RunProbe exits the process.
			health.RunProbe(health.DefaultPath)
			return
		case "scan":
			// One-shot scan+action, then exit. This is the trigger used by
			// an external scheduler (e.g. Ofelia `docker exec fclones
			// /app/wrapper scan`) when the built-in loop is disabled.
			// runScan/runFclonesJob already logs the failure with full
			// context, so exit non-zero here without a bare re-log.
			if err := runScan(context.Background()); err != nil {
				os.Exit(1)
			}
			return
		default:
			// An unrecognized subcommand is almost certainly a typo; fail
			// loudly instead of silently falling through to the daemon.
			// Mirrors the sibling schedulers (docker-renovate-scheduler /
			// docker-rsync-scheduler), which exit non-zero on an unknown
			// subcommand.
			setupLogger()
			slog.Error("unknown subcommand", "command", os.Args[1], "valid", "scan, health")
			os.Exit(2)
		}
	}

	// run/bootstrap already logs each failure once at the layer with the most
	// context, so exit non-zero here without a bare re-log.
	if err := run(context.Background()); err != nil {
		os.Exit(1)
	}
}

// run is the composition root for the long-running container. It wires
// dependencies and dispatches to the built-in scheduler or the idle
// external-trigger loop based on config. Returning an error exits non-zero.
func run(ctx context.Context) error {
	cfg, err := bootstrap(ctx)
	if err != nil {
		return err
	}

	// Reclaim report temp files orphaned in /cache by a previous hard-killed
	// scan. Safe here: the daemon has no scan in flight at startup.
	cleanStaleReports()

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	marker := health.NewMarker(health.DefaultPath)
	// The daemon owns the marker file for the container's lifetime, so it
	// cleans up on exit (unlike runScan, which deliberately leaves it).
	defer marker.Cleanup()

	if cfg.ScheduleEnabled {
		runBuiltin(ctx, marker, &cfg)
		return nil
	}
	runExternal(ctx, marker, &cfg)
	return nil
}

// bootstrap performs the shared startup prologue for both the long-running
// daemon (run) and the one-shot scan subcommand (runScan): it installs the
// logger, loads and validates config, and verifies the cache directory is
// writable. Each failure is logged exactly once at the layer with the most
// context — loadConfig logs the specific invalid setting at the leaf, and the
// cache-dir failure is logged here — so callers exit non-zero without
// re-logging.
func bootstrap(ctx context.Context) (config, error) {
	setupLogger()

	cfg, err := loadConfig()
	if err != nil {
		return config{}, err
	}

	if err := verifyCacheDir(ctx); err != nil {
		slog.Error("cache directory verification failed",
			"path", cacheDir, "uid", os.Getuid(), "error", err)
		return config{}, err
	}

	return cfg, nil
}

// runBuiltin runs the self-contained interval scheduler: a startup scan
// that fires immediately plus a ticker loop that fires every cfg.Interval.
// The flock in runFclonesJob guards against overlap if a scan runs longer
// than the interval. Both goroutines share the wait group so shutdown
// waits for in-flight work.
func runBuiltin(ctx context.Context, marker *health.Marker, cfg *config) {
	// Built-in mode starts unhealthy: clear any stale health file from a
	// previous run that crashed before its defer ran. The first successful
	// scan flips it to healthy.
	marker.Set(false)

	slog.Info("container started (built-in scheduling)",
		"uid", os.Getuid(), "interval", cfg.Interval,
		"target", cfg.ScanPath, "action", cfg.Action,
		"phase_timeout", cfg.PhaseTimeout)

	var wg sync.WaitGroup
	wg.Go(func() { _ = runFclonesJob(ctx, marker, cfg, "startup", defaultCommandRunner) })
	wg.Go(func() {
		ticker := time.NewTicker(cfg.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = runFclonesJob(ctx, marker, cfg, "interval", defaultCommandRunner)
			}
		}
	})

	<-ctx.Done()
	slog.Info("shutting down", "cause", context.Cause(ctx))
	// Mark unhealthy immediately so Grafana/Uptime Kuma see the signal
	// before the scan drain (which may take minutes on a large filesystem).
	marker.Set(false)

	// Wait for the startup scan and any in-flight ticker scan to drain.
	wg.Wait()
}

// runExternal idles until shutdown. The built-in scheduler is disabled
// (FCLONES_INTERVAL=off); scans are triggered out-of-band via `wrapper
// scan`. The marker is set healthy on boot so an idle, not-yet-triggered
// container reads healthy; each `scan` invocation updates it on disk.
func runExternal(ctx context.Context, marker *health.Marker, cfg *config) {
	// External (idle) mode starts healthy: an idle, not-yet-triggered
	// container reads healthy; each `scan` invocation updates it on disk.
	marker.Set(true)

	slog.Info("container started (external scheduling)",
		"uid", os.Getuid(), "target", cfg.ScanPath, "action", cfg.Action,
		"phase_timeout", cfg.PhaseTimeout, "trigger", "wrapper scan")

	<-ctx.Done()
	slog.Info("shutting down", "cause", context.Cause(ctx))
	// No marker.Set(false): unlike runBuiltin there is no in-flight scan to
	// drain, and run()'s deferred marker.Cleanup() removes the marker on exit.
}

// runScan performs exactly one scan+action and returns. It is the entry
// point for the `scan` subcommand invoked by an external scheduler. Unlike
// the daemon, it does not clean up the marker on exit — the file must
// persist so the running container's healthcheck reflects this run.
func runScan(ctx context.Context) error {
	// Construct the marker before bootstrap so a bootstrap failure -- most
	// importantly verifyCacheDir when /cache has gone read-only or full --
	// flips the running container's healthcheck unhealthy, honoring the
	// documented "becomes unhealthy when /cache is full or read-only" contract.
	// In external mode the daemon idles and only `wrapper scan` re-runs
	// bootstrap, so without this a runtime /cache failure exits non-zero for
	// the external scheduler yet leaves the healthcheck green. NewMarker probes
	// /tmp (DefaultPath), not /cache, so it still constructs fine here.
	marker := health.NewMarker(health.DefaultPath)

	cfg, err := bootstrap(ctx)
	if err != nil {
		marker.Set(false)
		return err
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Deliberately no `defer marker.Cleanup()` (unlike run): the marker file
	// must persist so the running container's healthcheck reflects this run.
	return runFclonesJob(ctx, marker, &cfg, "external", defaultCommandRunner)
}
