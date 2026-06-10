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
	setupLogger()

	cfg, err := loadConfig()
	if err != nil {
		slog.Error("config load failed", "error", err)
		return err
	}

	if err := verifyCacheDir(ctx); err != nil {
		slog.Error("cache directory verification failed",
			"path", cacheDir, "uid", os.Getuid(), "error", err)
		return err
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	marker := health.NewMarker(health.DefaultPath)
	defer marker.Cleanup()

	if cfg.ScheduleEnabled {
		runBuiltin(ctx, marker, &cfg)
		return nil
	}
	runExternal(ctx, marker, &cfg)
	return nil
}

// runBuiltin runs the self-contained interval scheduler: a startup scan
// that fires immediately plus a ticker loop that fires every cfg.Interval.
// The flock in runFclonesJob guards against overlap if a scan runs longer
// than the interval. Both goroutines share the wait group so shutdown
// waits for in-flight work.
func runBuiltin(ctx context.Context, marker *health.Marker, cfg *config) {
	// Clear any stale health file from a previous run that crashed before
	// its defer ran. The first successful scan flips it to healthy.
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
	marker.Set(true)

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
	setupLogger()

	cfg, err := loadConfig()
	if err != nil {
		slog.Error("config load failed", "error", err)
		return err
	}

	if err := verifyCacheDir(ctx); err != nil {
		slog.Error("cache directory verification failed",
			"path", cacheDir, "uid", os.Getuid(), "error", err)
		return err
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	marker := health.NewMarker(health.DefaultPath)
	return runFclonesJob(ctx, marker, &cfg, "external", defaultCommandRunner)
}
