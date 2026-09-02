package main

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/health"
	"github.com/cplieger/scheduler/v4"
	"github.com/cplieger/scheduler/v4/trigger"
)

// TestStartTickerDisabledInExternalMode pins that external mode runs no
// ticker: a ticker surviving into external mode would run unattended scans
// on a cadence the operator explicitly disabled, and with
// FCLONES_ACTION=remove those scans delete files.
//
// Runs in a synctest bubble because "no tick fired" is only probabilistic on
// a real clock: too short a sleep can't distinguish the invariant holding
// from a goroutine not yet scheduled.
func TestStartTickerDisabledInExternalMode(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		d := &daemon{queue: trigger.NewQueue[struct{}](queueCapacity), stamp: scheduler.NewStamp(filepath.Join(t.TempDir(), "last-run"))}

		done := startTicker(t.Context(), d, time.Millisecond, false, false)

		select {
		case <-done:
		default:
			t.Fatal("startTicker(enabled=false) channel not already closed, want a closed channel so runDaemon's shutdown wait resolves immediately")
		}

		synctest.Sleep(20 * time.Millisecond)

		if n := len(d.queue.Jobs()); n != 0 {
			t.Errorf("%d scan requests submitted in external mode, want 0", n)
		}
	})
}

// triggerLog records the trigger label of every run a fake executor serves.
type triggerLog struct {
	mu    sync.Mutex
	trigs []string
}

func (l *triggerLog) append(trig string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.trigs = append(l.trigs, trig)
}

func (l *triggerLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.trigs)
}

// startRecordingExecutor serves d.queue with the real executor loop and a
// run callback that only records the trigger label. The queue is closed in
// cleanup so the executor exits before the bubble checks for stragglers.
func startRecordingExecutor(t *testing.T, d *daemon) *triggerLog {
	t.Helper()
	rec := &triggerLog{}
	go trigger.Execute(t.Context(), d.queue, func(_ context.Context, trig string, _ struct{}) trigger.Outcome {
		rec.append(trig)
		return trigger.Outcome{OK: true}
	})
	t.Cleanup(d.queue.Close)
	return rec
}

// TestStartTickerFiresStartupScanWhenDue pins the due half of the
// conditional startup fire: a due record submits one startup-labeled run
// before the interval loop, and the loop's own ticks are labeled interval.
func TestStartTickerFiresStartupScanWhenDue(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		d := &daemon{queue: trigger.NewQueue[struct{}](queueCapacity), stamp: scheduler.NewStamp(filepath.Join(t.TempDir(), "last-run"))}
		rec := startRecordingExecutor(t, d)

		startTicker(t.Context(), d, time.Hour, true, true)

		synctest.Wait()
		if got, want := rec.snapshot(), []string{triggerStartup}; !slices.Equal(got, want) {
			t.Errorf("triggers after boot = %v, want %v", got, want)
		}

		synctest.Sleep(time.Hour + time.Minute)
		if got, want := rec.snapshot(), []string{triggerStartup, triggerInterval}; !slices.Equal(got, want) {
			t.Errorf("triggers after one interval = %v, want %v", got, want)
		}
	})
}

// TestStartTickerSkipsStartupScanWhenNotDue pins the skip half: with a
// fresh record no run fires at boot, and the first scan is the interval
// tick at most one interval later — which is also the retry slot after a
// fresh FAILED record under CountFailed.
func TestStartTickerSkipsStartupScanWhenNotDue(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		d := &daemon{queue: trigger.NewQueue[struct{}](queueCapacity), stamp: scheduler.NewStamp(filepath.Join(t.TempDir(), "last-run"))}
		rec := startRecordingExecutor(t, d)

		startTicker(t.Context(), d, time.Hour, true, false)

		synctest.Wait()
		if got := rec.snapshot(); len(got) != 0 {
			t.Errorf("triggers after boot = %v, want none (startup scan not due)", got)
		}

		synctest.Sleep(time.Hour + time.Minute)
		if got, want := rec.snapshot(), []string{triggerInterval}; !slices.Equal(got, want) {
			t.Errorf("triggers after one interval = %v, want %v", got, want)
		}
	})
}

// TestBuiltinBootState pins the boot decision pair the /cache record
// drives. The fresh-FAILED case is the CountFailed policy's signature:
// the startup scan is skipped (any completed scan holds its schedule slot;
// the interval ticker owns the retry) AND the marker boots unhealthy,
// because not-due does not imply the last scan succeeded.
func TestBuiltinBootState(t *testing.T) {
	t.Parallel()
	const interval = time.Hour
	tests := []struct {
		name        string
		hasRecord   bool
		recordOK    bool
		age         time.Duration
		wantDue     bool
		wantHealthy bool
	}{
		{name: "no record fires the startup scan and boots unhealthy", wantDue: true},
		{name: "fresh success skips the startup scan and boots healthy", hasRecord: true, recordOK: true, wantHealthy: true},
		{name: "fresh FAILED skips the startup scan and boots unhealthy", hasRecord: true},
		{name: "stale success fires the startup scan", hasRecord: true, recordOK: true, age: 2 * interval, wantDue: true},
		{name: "stale failure fires the startup scan", hasRecord: true, age: 2 * interval, wantDue: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stamp := scheduler.NewStamp(filepath.Join(t.TempDir(), "last-run"))
			if tt.hasRecord {
				if err := stamp.Record(tt.recordOK); err != nil {
					t.Fatalf("Record(%v): %v", tt.recordOK, err)
				}
			}
			now := time.Now().Add(tt.age)

			due, healthy, last := builtinBootState(stamp, interval, now)
			if due != tt.wantDue {
				t.Errorf("builtinBootState(record=%v/%v, age=%s) due = %v, want %v",
					tt.hasRecord, tt.recordOK, tt.age, due, tt.wantDue)
			}
			if healthy != tt.wantHealthy {
				t.Errorf("builtinBootState(record=%v/%v, age=%s) healthy = %v, want %v",
					tt.hasRecord, tt.recordOK, tt.age, healthy, tt.wantHealthy)
			}
			if tt.hasRecord && last.OK != tt.recordOK {
				t.Errorf("builtinBootState last.OK = %v, want the recorded %v", last.OK, tt.recordOK)
			}
		})
	}
}

// TestDaemonRecordOutcome pins which runs reach the health marker and the
// /cache stamp: a completed scheduled run records both with its outcome, a
// triggered run updates health only (it does not answer the startup
// freshness question), and a lock skip or a cancelled run touches neither.
func TestDaemonRecordOutcome(t *testing.T) {
	t.Parallel()
	tests := []struct {
		ctxErr      error
		runErr      error
		name        string
		trig        string
		ran         bool
		preHealthy  bool
		wantHealthy bool
		wantStamp   bool
		wantStampOK bool
	}{
		{
			name: "startup success records healthy marker and ok stamp",
			trig: triggerStartup, ran: true,
			wantHealthy: true, wantStamp: true, wantStampOK: true,
		},
		{
			name: "interval failure records unhealthy marker and failed stamp",
			trig: triggerInterval, ran: true, runErr: errors.New("scan failed"),
			preHealthy: true, wantStamp: true,
		},
		{
			name: "external trigger updates health but never the stamp",
			trig: trigger.TriggerExternal, ran: true,
			wantHealthy: true,
		},
		{
			name:       "lock skip touches neither",
			trig:       triggerInterval,
			preHealthy: true, wantHealthy: true,
		},
		{
			name: "cancelled run touches neither",
			trig: triggerStartup, ran: true, ctxErr: context.Canceled, runErr: context.Canceled,
			preHealthy: true, wantHealthy: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			marker := health.NewMarker(filepath.Join(dir, "healthy"))
			marker.Set(tt.preHealthy)
			d := &daemon{
				health: health.NewLatch(marker),
				stamp:  scheduler.NewStamp(filepath.Join(dir, "last-run")),
			}

			d.recordOutcome(tt.trig, tt.ctxErr, tt.ran, tt.runErr)

			if got := marker.CheckHealthy(); got != tt.wantHealthy {
				t.Errorf("recordOutcome(%q) marker healthy = %v, want %v", tt.trig, got, tt.wantHealthy)
			}
			rec, known := d.stamp.Last()
			if known != tt.wantStamp {
				t.Errorf("recordOutcome(%q) stamp recorded = %v, want %v", tt.trig, known, tt.wantStamp)
			}
			if known && rec.OK != tt.wantStampOK {
				t.Errorf("recordOutcome(%q) stamp OK = %v, want %v", tt.trig, rec.OK, tt.wantStampOK)
			}
		})
	}
}

// A stamp write failure warns and demotes nothing: the marker keeps the
// run's real outcome, and the absent record merely reads as due at the next
// boot. Swaps the process-global logger, so no t.Parallel.
func TestDaemonRecordOutcomeStampWriteFailureWarns(t *testing.T) {
	orig := slog.Default()
	t.Cleanup(func() { slog.SetDefault(orig) })
	var logs strings.Builder
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))

	dir := t.TempDir()
	marker := health.NewMarker(filepath.Join(dir, "healthy"))
	d := &daemon{
		health: health.NewLatch(marker),
		stamp:  scheduler.NewStamp(filepath.Join(dir, "missing-dir", "last-run")),
	}

	d.recordOutcome(triggerInterval, nil, true, nil)

	if !marker.CheckHealthy() {
		t.Error("marker healthy = false after a stamp write failure, want true (the failure must not demote the run)")
	}
	got := logs.String()
	if !strings.Contains(got, "level=WARN") || !strings.Contains(got, "cannot record the scan outcome") {
		t.Errorf("stamp write failure logged %q, want a WARN carrying \"cannot record the scan outcome\"", got)
	}
}
