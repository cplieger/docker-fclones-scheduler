package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/cplieger/go/health"
	"github.com/cplieger/go/scheduler"
	"github.com/cplieger/go/scheduler/trigger"
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

// Trigger labels for the daemon's own scheduled runs. Only these record the
// /cache stamp: a triggered run (trigger.TriggerExternal) is out-of-band and
// does not answer built-in mode's startup-freshness question.
const (
	triggerStartup  = "startup"
	triggerInterval = "interval"
)

// daemon carries the executor's dependencies.
type daemon struct {
	queue *trigger.Queue[struct{}]
	// health holds the marker: bootDaemon writes the starting verdict, the
	// reporter each completed run's, and shutdown begins the drain.
	health   *health.Latch
	reporter *reporter
	cfg      *config
	// stamp is the restart-surviving last-run record on /cache.
	stamp  *scheduler.Stamp
	newCmd scheduler.CommandRunner
}

// runDaemon runs the two long-running modes (built-in and external): binds
// the trigger socket, wires the health latch, starts the executor, and
// — in built-in mode — drives the interval ticker. Returning an error exits
// non-zero.
func runDaemon(ctx context.Context, cfg *config, state *health.Latch, rep *reporter, newCmd scheduler.CommandRunner) error {
	ln, err := trigger.Listen(socketPath)
	if err != nil {
		slog.Error("cannot bind trigger socket", "path", socketPath, "error", err)
		state.Set(false)
		return err
	}
	defer func() { _ = os.Remove(socketPath) }()

	stamp := scheduler.NewStamp(stampFile)
	builtin := cfg.Mode == modeBuiltin
	startupDue := bootDaemon(cfg, stamp, rep, state, time.Now())

	d := &daemon{
		queue:    trigger.NewQueue[struct{}](queueCapacity),
		health:   state,
		reporter: rep,
		cfg:      cfg,
		stamp:    stamp,
		newCmd:   newCmd,
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
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = srv.Serve(ctx, ln)
	}()

	tickerDone := startTicker(ctx, d, cfg.Interval, builtin, startupDue)

	bootAttrs := []any{
		"uid", os.Getuid(), "mode", cfg.Mode, "interval", cfg.Interval,
		"target", cfg.ScanPath, "action", cfg.Action,
		"phase_timeout", cfg.PhaseTimeout, "socket", socketPath,
		"metrics_listen_addr", cfg.MetricsListenAddr,
	}
	if builtin {
		bootAttrs = append(bootAttrs, "startup_scan", startupDue)
	}
	slog.Info("container started ("+cfg.Mode.String()+" scheduling)", bootAttrs...)

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
	<-served
	slog.Info("shutdown complete")
	return nil
}

// bootDaemon sets the marker's starting verdict and reports whether built-in
// mode's startup scan is due. The schedule stamp decides only that scan, under
// CountFailed so a failed multi-hour scan is not repeated on every restart.
// The verdict is the last completed run's, the record last_run_ok is seeded
// from, so the two agree; a due startup scan boots unhealthy.
func bootDaemon(cfg *config, schedule *scheduler.Stamp, rep *reporter, state *health.Latch, now time.Time) (startupDue bool) {
	builtin := cfg.Mode == modeBuiltin
	last, _ := schedule.Last()
	// With no completed-run record yet, external mode is idle with nothing
	// failed and built-in mode falls back to the schedule stamp.
	healthy := !builtin || last.OK
	if completed, known := rep.completed.Last(); known {
		healthy = completed.OK
	}
	if builtin {
		startupDue = schedule.Due(cfg.Interval, now, scheduler.CountFailed)
		if startupDue {
			healthy = false
		} else {
			slog.Info("startup scan skipped, a completed scan survived the restart",
				"completed", last.Time, "healthy", healthy)
		}
	}
	state.Set(healthy)
	return startupDue
}

// startTicker runs the built-in interval scheduler: when the /cache record
// says a scan is due, a startup run fires immediately; either way one run
// then fires per interval. Disabled (closed channel returned) in external
// mode.
func startTicker(ctx context.Context, d *daemon, interval time.Duration, enabled, startupDue bool) <-chan struct{} {
	done := make(chan struct{})
	if !enabled {
		close(done)
		return done
	}
	go func() {
		defer close(done)
		if startupDue {
			d.tick(triggerStartup)
		}
		scheduler.RunLoop(ctx, func(context.Context) {
			d.tick(triggerInterval)
		}, scheduler.LoopOptions{
			Interval:    interval,
			FireOnStart: false,
			// Phase the first tick from the recorded previous scan, so a
			// restart neither adds a scan nor delays the cadence.
			FirstDelay: d.stamp.Remaining(interval, time.Now(), scheduler.CountFailed),
		})
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

// run performs one request: run the scan+action job, report the outcome,
// and return the result. Runs under the shutdown-cancellable ctx on purpose:
// SIGTERM interrupts an in-flight fclones phase, and jobHealthSignal's
// interrupted-run carve-out keeps that drain from registering as a failure. A
// cross-container lock skip (ran=false, no error) performed no scan and
// reports nothing.
func (d *daemon) run(ctx context.Context, trig string, _ struct{}) trigger.Result {
	start := time.Now()

	rep := runFclonesJob(ctx, d.cfg, trig, d.newCmd)
	d.recordOutcome(ctx.Err(), &rep)

	out := trigger.Result{OK: rep.err == nil, Duration: time.Since(start)}
	if !rep.ran && rep.err == nil {
		out.Reason = "skipped: scan lock held by another process sharing /cache"
	}
	return out
}

// recordOutcome reports a finished run through the reporter and, for the
// daemon's own scheduled runs, records the /cache stamp. A cancelled run or a
// cross-container lock skip completed no scan, so it reaches neither. A stamp
// write failure never affects the run: an absent record reads as due, costing
// at most one extra startup scan after the next recreate.
func (d *daemon) recordOutcome(ctxErr error, rep *jobReport) {
	completed, healthy := d.reporter.report(ctxErr, rep)
	if !completed {
		return
	}
	if rep.trigger != triggerStartup && rep.trigger != triggerInterval {
		return
	}
	if err := d.stamp.Record(healthy); err != nil {
		slog.Warn("cannot record the scan outcome on /cache; the next restart may repeat a scan this record would have skipped",
			"error", err)
	}
}
