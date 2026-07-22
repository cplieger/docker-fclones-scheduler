package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/cplieger/scheduler/v3"
	"github.com/cplieger/scheduler/v3/trigger"
)

// --- Daemon: the single owner of scan execution ---
//
// In the two long-running modes, PID 1 owns every run. Triggers only submit
// requests: the built-in ticker (built-in mode) and the unix-socket clients
// (`scan` subcommand, both modes) all feed one FIFO queue served by one
// executor goroutine. That single-ownership is the design: in-process mutual
// exclusion is the executor loop, shutdown cancels the in-flight run's
// context so fclones drains under the existing SIGTERM-then-grace machinery,
// and every run's log lines land on the container log stream because the run
// executes in the daemon — in external mode too, which the previous
// exec-child design could not offer. The /cache flock inside runFclonesJob
// remains as the cross-container guard (a manual `docker run` sharing the
// same /cache volume), demoted from correctness mechanism to belt and
// braces. The queue, socket server, and wire protocol are the scheduler
// library's trigger broker (`scheduler/v2/trigger`); this file wires it and
// owns the policy — executor semantics and log wording.

// socketPath is the daemon's trigger socket. /tmp is per-container tmpfs, so
// the path never collides across containers and a stale file can only be our
// own previous life's.
const socketPath = "/tmp/fclones-wrapper.sock"

// queueCapacity bounds pending requests in the trigger broker's FIFO. The
// realistic trigger set is one periodic trigger (Ofelia) plus a manual exec,
// so 16 is generous headroom; a client hitting a full queue is rejected
// immediately with a clear reason (honest backpressure) rather than queued
// unboundedly.
const queueCapacity = 16

// daemon carries the executor's dependencies.
type daemon struct {
	queue *trigger.Queue[struct{}]
	// hc is the single writer of the health marker; every run outcome
	// funnels through it (drain latch, interrupted-clean carve-out,
	// lock-skip no-signal).
	hc     *healthController
	cfg    *config
	newCmd scheduler.CommandRunner
}

// runDaemon runs the two long-running modes (built-in and external): it
// binds the trigger socket, wires the health controller, starts the
// executor, and — in built-in mode — drives the interval ticker. The config
// is env-derived and immutable for the container's life, so unlike the
// sibling schedulers' file configs there is nothing to reload per run.
// Returning an error exits non-zero.
func runDaemon(ctx context.Context, cfg *config, hc *healthController, newCmd scheduler.CommandRunner) error {
	ln, err := trigger.Listen(socketPath)
	if err != nil {
		slog.Error("cannot bind trigger socket", "path", socketPath, "error", err)
		hc.markUnhealthy()
		return err
	}
	defer func() { _ = os.Remove(socketPath) }()

	// Built-in mode starts unhealthy until the first run proves the setup
	// (the startup run flips it); external mode starts healthy — idle,
	// nothing has failed — and each triggered run updates it.
	hc.markInitial(cfg.Mode == modeExternal)

	d := &daemon{
		queue:  trigger.NewQueue[struct{}](queueCapacity),
		hc:     hc,
		cfg:    cfg,
		newCmd: newCmd,
	}

	executorDone := make(chan struct{})
	go func() {
		defer close(executorDone)
		trigger.Execute(ctx, d.queue, d.run)
	}()

	// The broker owns the wire (decode, event relay, handler draining); the
	// hook only supplies this app's log vocabulary. A scan takes no
	// arguments, so there is nothing app-specific to say on rejection — the
	// library's payload-free warning matches the previous wording.
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
	// Latch unhealthy immediately so observers see the drain before the
	// in-flight run resolves (it is being SIGTERM'd via ctx and drains under
	// the runner's grace window; the latch also blocks a late healthy write).
	d.hc.beginDrain()

	// Stop admission (socket + queue), then wait: the executor delivers the
	// interrupted in-flight run's result and cancellation results to
	// everything still queued; the ticker returns once its waiting tick
	// request resolves; the handlers return once every accepted request has
	// its final event on the wire.
	_ = ln.Close()
	d.queue.Close()
	<-executorDone
	<-tickerDone
	srv.Wait()
	slog.Info("shutdown complete")
	return nil
}

// startTicker runs the built-in interval scheduler: a startup run that fires
// immediately for freshness on deploy, then one run per interval, each
// submitted to the queue like any other trigger and waited on (RunLoop is
// sequential, so ticks can never pile up behind a long run). Disabled
// (closed channel returned) in external mode. The library re-checks ctx
// before each fire, so no fresh tick is submitted after shutdown begins.
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

// tick submits one scheduled run and waits for its result (the executor
// writes the health marker; the queue guarantees exactly one result per
// accepted request, including a cancellation result at shutdown, so this
// wait always resolves). A rejected submission — the queue full of external
// requests, or shutdown racing the tick — is logged and skipped: the next
// interval provides freshness.
func (d *daemon) tick(trig string) {
	j := trigger.NewJob(trig, struct{}{})
	if err := d.queue.Submit(j); err != nil {
		slog.Warn("scheduled scan skipped", "trigger", trig, "reason", err)
		return
	}
	<-j.Result()
}

// run performs one request: run the scan+action job, route the outcome
// through the health controller, and return the result. The job lifecycle
// around it belongs to trigger.Execute — the daemon's single executor loop —
// which checks the shutdown ctx before each start (queued requests behind a
// stop are cancelled with an explicit result, never run) and guarantees
// exactly one delivered result per accepted request, even if this callback
// panics.
//
// The run executes under the shutdown-cancellable ctx on purpose: SIGTERM
// interrupts an in-flight fclones phase (SIGTERM-then-grace via the command
// runner), and jobHealthSignal's interrupted-run carve-out keeps that drain
// from registering as a failure. A cross-container lock skip (ran=false, no
// error) performed no scan: it writes no marker and reports ok with a
// reason, preserving the documented overlap tolerance for external triggers.
func (d *daemon) run(ctx context.Context, trig string, _ struct{}) trigger.Outcome {
	start := time.Now()

	ran, err := runFclonesJob(ctx, d.cfg, trig, d.newCmd)
	if set, healthy := jobHealthSignal(ctx.Err(), ran, err); set {
		d.hc.apply(healthy)
	}

	out := trigger.Outcome{OK: err == nil, Duration: time.Since(start)}
	if !ran && err == nil {
		out.Reason = "skipped: scan lock held by another process sharing /cache"
	}
	return out
}
