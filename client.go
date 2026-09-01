package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/cplieger/scheduler/v4/trigger"
)

// The `scan` subcommand: a thin trigger client over the scheduler library.
// The run executes inside the daemon (its logs land on the container's log
// stream); this file only submits the request and reports the result. The
// client never touches the health marker — the daemon is its single writer.

// runClient performs one triggered run via the daemon at socketPath and
// returns the process exit code: 0 on success (including the documented
// cross-container lock-skip tolerance), 1 on failure (including a rejected
// or cancelled request, or a daemon that cannot be reached).
func runClient(socketPath string) int {
	setupLogger()

	// Unbounded wait by contract; bind to the terminal so an interrupted
	// `docker exec` closes the connection instead of leaving the socket open.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	final, err := trigger.Submit(ctx, socketPath, struct{}{}, func(ev trigger.Event) {
		switch ev.Kind {
		case trigger.EventQueued:
			slog.Info("triggered scan accepted")
		case trigger.EventStarted:
			slog.Info("triggered scan started",
				"logs", "full scan output is on the container log stream")
		}
	})
	switch {
	case errors.Is(err, trigger.ErrUnreachable):
		slog.Error("cannot reach the scheduler daemon",
			"path", socketPath, "error", err,
			"hint", "the daemon (PID 1) owns all runs; check the container is up and this exec runs as the container's user (the socket is owner-only)")
		return 1
	case errors.Is(err, trigger.ErrSend):
		slog.Error("cannot send scan request", "error", err)
		return 1
	case errors.Is(err, context.Canceled):
		slog.Warn("interrupted while waiting for the scan; it continues in the daemon")
		return 1
	case err != nil:
		slog.Error("connection lost before the run completed (daemon stopped?)", "error", err)
		return 1
	}
	return finishResult(final)
}

// finishResult logs the final outcome and maps it to the exit code.
func finishResult(ev trigger.Event) int {
	if ev.OK {
		attrs := []any{"duration_ms", ev.DurationMs}
		if ev.Reason != "" {
			attrs = append(attrs, "reason", ev.Reason)
		}
		slog.Info("triggered scan complete", attrs...)
		return 0
	}
	reason := ev.Reason
	if reason == "" {
		reason = "the run failed (see the container log stream)"
	}
	slog.Error("triggered scan failed", "duration_ms", ev.DurationMs, "reason", reason)
	return 1
}
