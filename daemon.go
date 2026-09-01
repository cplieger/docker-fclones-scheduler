package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/cplieger/health"
	"github.com/cplieger/scheduler/v4"
	"github.com/cplieger/scheduler/v4/trigger"
)

// In the two long-running modes, PID 1 owns every run: the built-in ticker
// and the unix-socket clients (`scan` subcommand) feed one FIFO queue served
// by one executor goroutine, so in-process mutual exclusion is the executor
// loop itself. The /cache flock inside runFclonesJob remains as the
// cross-container guard (a manual `docker run` sharing the same volume).
// The queue, socket server, and wire protocol are the scheduler library's
// trigger broker; this file wires it and owns the log wording.

// socketPath is the daemon's trigger socket. /tmp is per-container tmpfs, so
// a stale file can only be our own previous life's.
const socketPath = "/tmp/fclones-wrapper.sock"

// queueCapacity bounds pending requests; a full queue rejects immediately
// with a clear reason rather than queueing unboundedly.
const queueCapacity = 16

// daemon carries the executor's dependencies.
type daemon struct {
	queue *trigger.Queue[struct{}]
	// health is the single writer of the marker; every run outcome funnels through it.
	health *health.Latch
	cfg    *config
	newCmd scheduler.CommandRunner
}

// runDaemon runs the two long-running modes (built-in and external): binds
// the trigger socket, wires the health latch, starts the executor, and
// — in built-in mode — drives the interval ticker. Returning an error exits
// non-zero.
func runDaemon(ctx context.Context, cfg *config, state *health.Latch, newCmd scheduler.CommandRunner) error {
	ln, err := trigger.Listen(socketPath)
	if err != nil {
		slog.Error("cannot bind trigger socket", "path", socketPath, "error", err)
		state.Set(false)
		return err
	}
	defer func() { _ = os.Remove(socketPath) }()

	// Built-in mode starts unhealthy until the first run proves the setup;
	// external mode starts healthy (idle, nothing has failed).
	state.Set(cfg.Mode == modeExternal)

	d := &daemon{
		queue:  trigger.NewQueue[struct{}](queueCapacity),
		health: state,
		cfg:    cfg,
		newCmd: newCmd,
	}

	executorDone := make(chan struct{})
	go func() {
		defer close(executorDone)
		trigger.Execute(ctx, d.queue, d.run)
	}()

	// The broker owns the wire; the hook only supplies this app's log
	// wording. A scan takes no arguments, so nothing app-specific to say.
	srv := &trigger.Server[struct{}]{
		Queue: d.queue,
		OnAccepted: func(struct{}) {
			slog.Info("triggered scan queued")
		},
	}
	srv.Serve(ln)

	tickerDone := startTicker(ctx, d, cfg.Interval, cfg.Mode == modeBuiltin)

	slog.Info("container started ("+cfg.Mode.String()+" scheduling)",
		"uid", os.Getuid(), "mode", cfg.Mode, "interval", cfg.Interval,
		"target", cfg.ScanPath, "action", cfg.Action,
		"phase_timeout", cfg.PhaseTimeout, "socket", socketPath)

	<-ctx.Done()
	slog.Info("shutting down", "cause", context.Cause(ctx))
	// Latch unhealthy before the in-flight run resolves so observers see the
	// drain, and to block a late healthy write.
	d.health.BeginDrain()

	// Stop admission, then wait for the executor, ticker and handlers to
	// finish delivering results to everything already accepted.
	_ = ln.Close()
	d.queue.Close()
	<-executorDone
	<-tickerDone
	srv.Wait()
	slog.Info("shutdown complete")
	return nil
}

// startTicker runs the built-in interval scheduler: a startup run fires
// immediately, then one run per interval, each submitted to the queue and
// waited on. Disabled (closed channel returned) in external mode.
func startTicker(ctx context.Context, d *daemon, interval time.Duration, enabled bool) <-chan struct{} {
	done := make(chan struct{})
	if !enabled {
		close(done)
		return done
	}
	go func() {
		defer close(done)
		startupDone := false
		scheduler.RunLoop(ctx, func(context.Context) {
			trig := "interval"
			if !startupDone {
				trig, startupDone = "startup", true
			}
			d.tick(trig)
		}, scheduler.LoopOptions{Interval: interval, FireOnStart: true})
	}()
	return done
}

// tick submits one scheduled run and waits for its result. A rejected
// submission (queue full, or shutdown racing the tick) is logged and
// skipped: the next interval provides freshness.
func (d *daemon) tick(trig string) {
	j := trigger.NewJob(trig, struct{}{})
	if err := d.queue.Submit(j); err != nil {
		slog.Warn("scheduled scan skipped", "trigger", trig, "reason", err)
		return
	}
	<-j.Result()
}

// run performs one request: run the scan+action job, route the outcome
// through the health latch, and return the result. Runs under the
// shutdown-cancellable ctx on purpose: SIGTERM interrupts an in-flight
// fclones phase, and jobHealthSignal's interrupted-run carve-out keeps that
// drain from registering as a failure. A cross-container lock skip
// (ran=false, no error) performed no scan and writes no marker.
func (d *daemon) run(ctx context.Context, trig string, _ struct{}) trigger.Outcome {
	start := time.Now()

	ran, err := runFclonesJob(ctx, d.cfg, trig, d.newCmd)
	if set, healthy := jobHealthSignal(ctx.Err(), ran, err); set {
		d.health.Set(healthy)
	}

	out := trigger.Outcome{OK: err == nil, Duration: time.Since(start)}
	if !ran && err == nil {
		out.Reason = "skipped: scan lock held by another process sharing /cache"
	}
	return out
}
