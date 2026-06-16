package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

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
			if err := runScan(context.Background()); err != nil {
				slog.Error("scan failed", "error", err)
				os.Exit(1)
			}
			return
		}
	}

	if err := run(context.Background()); err != nil {
		slog.Error("fatal", "error", err)
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
// writable. Each failure is logged with the existing context before the
// error is returned to the caller, which exits the process non-zero.
func bootstrap(ctx context.Context) (config, error) {
	setupLogger()

	cfg, err := loadConfig()
	if err != nil {
		slog.Error("config load failed", "error", err)
		return config{}, err
	}

	if err := verifyCacheDir(ctx); err != nil {
		slog.Error("cache directory verification failed",
			"path", cacheDir, "uid", os.Getuid(), "error", err)
		return config{}, err
	}

	return cfg, nil
}

// initMarker sets the health marker's boot state for a daemon mode. The
// built-in scheduler boots unhealthy (healthy=false) and flips healthy after
// its first successful scan; the external idle loop boots healthy
// (healthy=true) since nothing has failed yet. Shutdown sets the marker
// unhealthy in both modes (see runBuiltin/runExternal).
func initMarker(marker *health.Marker, healthy bool) {
	marker.Set(healthy)
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
	initMarker(marker, false)

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
	initMarker(marker, true)

	slog.Info("container started (external scheduling)",
		"uid", os.Getuid(), "target", cfg.ScanPath, "action", cfg.Action,
		"phase_timeout", cfg.PhaseTimeout, "trigger", "wrapper scan")

	<-ctx.Done()
	slog.Info("shutting down", "cause", context.Cause(ctx))
	marker.Set(false)
}

// runScan performs exactly one scan+action and returns. It is the entry
// point for the `scan` subcommand invoked by an external scheduler. Unlike
// the daemon, it does not clean up the marker on exit — the file must
// persist so the running container's healthcheck reflects this run.
func runScan(ctx context.Context) error {
	cfg, err := bootstrap(ctx)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Deliberately no `defer marker.Cleanup()` (unlike run): the marker file
	// must persist so the running container's healthcheck reflects this run.
	marker := health.NewMarker(health.DefaultPath)
	return runFclonesJob(ctx, marker, &cfg, "external", defaultCommandRunner)
}
