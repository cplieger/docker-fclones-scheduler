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
	// CLI health probe for Docker healthcheck (distroless has no curl/wget).
	// Checks for a marker file instead of making an HTTP request — no port needed.
	if len(os.Args) > 1 && os.Args[1] == "health" {
		health.RunProbe(health.DefaultPath)
	}

	if err := run(context.Background()); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

// run is the composition root: it wires all dependencies and blocks until
// shutdown. Returning an error causes main() to exit non-zero.
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

	// Remove stale health file from a previous run that may have crashed
	// before its defer ran. The first successful scan will flip it to healthy.
	marker := health.NewMarker(health.DefaultPath)
	marker.Set(false)
	defer marker.Cleanup()

	js := &jobSlot{}

	slog.Info("container started",
		"uid", os.Getuid(), "interval", cfg.Interval,
		"target", cfg.ScanPath, "action", cfg.Action,
		"phase_timeout", cfg.PhaseTimeout)

	// Two goroutines: a startup scan that fires immediately, and a
	// ticker-driven loop that fires every cfg.Interval. The jobSlot
	// guards against overlap if a scan runs longer than the interval.
	// Both share the wait group so shutdown waits for in-flight work.
	var wg sync.WaitGroup
	wg.Go(func() { runFclonesJob(ctx, marker, &cfg, "startup", js, defaultCommandRunner) })
	wg.Go(func() {
		ticker := time.NewTicker(cfg.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runFclonesJob(ctx, marker, &cfg, "interval", js, defaultCommandRunner)
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
	return nil
}
