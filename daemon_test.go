package main

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/go/health"
	"github.com/cplieger/go/scheduler"
	"github.com/cplieger/go/scheduler/trigger"
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
	go trigger.Execute(t.Context(), d.queue, func(_ context.Context, trig string, _ struct{}) trigger.Result {
		rec.append(trig)
		return trigger.Result{OK: true}
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

// TestBootDaemon pins the two boot calls. The schedule stamp decides only
// built-in mode's startup scan, under scheduler.CountFailed: a fresh FAILED
// scheduled scan skips the startup scan. The marker starts from the last
// completed run of any trigger, so a triggered run the schedule stamp never
// saw still decides it.
func TestBootDaemon(t *testing.T) {
	t.Parallel()
	const interval = time.Hour
	type rec struct {
		age time.Duration
		ok  bool
	}
	tests := []struct {
		schedule    *rec
		completed   *rec
		name        string
		mode        runMode
		wantDue     bool
		wantHealthy bool
	}{
		{name: "built-in with no record fires the startup scan and boots unhealthy", mode: modeBuiltin, wantDue: true},
		{name: "built-in fresh success skips the startup scan and boots healthy", mode: modeBuiltin, schedule: &rec{ok: true}, completed: &rec{ok: true}, wantHealthy: true},
		{name: "built-in fresh FAILED skips the startup scan and boots unhealthy", mode: modeBuiltin, schedule: &rec{}, completed: &rec{}},
		{name: "built-in stale success fires the startup scan", mode: modeBuiltin, schedule: &rec{ok: true, age: 2 * interval}, completed: &rec{ok: true, age: 2 * interval}, wantDue: true},
		{name: "built-in stale failure fires the startup scan", mode: modeBuiltin, schedule: &rec{age: 2 * interval}, wantDue: true},
		{name: "built-in scheduled success then a failed triggered run boots unhealthy", mode: modeBuiltin, schedule: &rec{ok: true}, completed: &rec{}},
		{name: "built-in scheduled failure then a good triggered run boots healthy", mode: modeBuiltin, schedule: &rec{}, completed: &rec{ok: true}, wantHealthy: true},
		{name: "built-in due startup scan boots unhealthy after a good triggered run", mode: modeBuiltin, completed: &rec{ok: true}, wantDue: true},
		{name: "built-in with only the schedule stamp boots from it", mode: modeBuiltin, schedule: &rec{ok: true}, wantHealthy: true},
		{name: "external with no completed run boots healthy", mode: modeExternal, wantHealthy: true},
		{name: "external after a failed run boots unhealthy", mode: modeExternal, completed: &rec{}},
		{name: "external after a good run boots healthy", mode: modeExternal, completed: &rec{ok: true}, wantHealthy: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			schedule := scheduler.NewStamp(filepath.Join(dir, "last-run"))
			completed := scheduler.NewStamp(filepath.Join(dir, "last-completed"))
			for _, s := range []struct {
				r     *rec
				stamp *scheduler.Stamp
			}{{tt.schedule, schedule}, {tt.completed, completed}} {
				if s.r == nil {
					continue
				}
				if err := s.stamp.Record(s.r.ok); err != nil {
					t.Fatalf("Record(%v): %v", s.r.ok, err)
				}
			}
			// Both records are written now; age them by booting later.
			age := time.Duration(0)
			for _, r := range []*rec{tt.schedule, tt.completed} {
				if r != nil {
					age = max(age, r.age)
				}
			}
			marker := health.NewMarker(filepath.Join(dir, "healthy"))
			cfg := &config{Mode: tt.mode, Interval: interval}

			due := bootDaemon(cfg, schedule, &reporter{completed: completed}, health.NewLatch(marker), time.Now().Add(age))
			if due != tt.wantDue {
				t.Errorf("bootDaemon startup due = %v, want %v", due, tt.wantDue)
			}
			if got := marker.CheckHealthy(); got != tt.wantHealthy {
				t.Errorf("bootDaemon marker healthy = %v, want %v", got, tt.wantHealthy)
			}
		})
	}
}

// TestBootVerdictMatchesTheRestoredLastRun pins that after a restart the
// marker and last_run_ok give the same verdict before any run.
func TestBootVerdictMatchesTheRestoredLastRun(t *testing.T) {
	for _, mode := range []runMode{modeExternal, modeBuiltin} {
		for _, ok := range []bool{true, false} {
			t.Run(mode.String()+" last run ok "+strconv.FormatBool(ok), func(t *testing.T) {
				dir := t.TempDir()
				prevStamp := completedStampPath
				completedStampPath = filepath.Join(dir, "last-completed")
				t.Cleanup(func() { completedStampPath = prevStamp })
				schedule := scheduler.NewStamp(filepath.Join(dir, "last-run"))
				// The schedule stamp holds the opposite verdict: a scheduled run
				// followed by a triggered one.
				if err := schedule.Record(!ok); err != nil {
					t.Fatalf("Record: %v", err)
				}
				if err := scheduler.NewStamp(completedStampPath).Record(ok); err != nil {
					t.Fatalf("Record: %v", err)
				}
				marker := health.NewMarker(filepath.Join(dir, "healthy"))
				latch := health.NewLatch(marker)
				cfg := &config{Mode: mode, Interval: time.Hour}

				reg, rep := newReporter(cfg, latch)
				bootDaemon(cfg, schedule, rep, latch, time.Now())
				if got := marker.CheckHealthy(); got != ok {
					t.Errorf("marker healthy after restart = %v, want the last run's %v", got, ok)
				}
				if got := gauge(t, reg, "last_run_ok"); got != float64(flag(ok)) {
					t.Errorf("last_run_ok after restart = %v, want %d", got, flag(ok))
				}
			})
		}
	}
}

// TestStartupSkipLogsTheBootVerdict pins that the startup-skip line reports
// the verdict the marker boots with, not the schedule stamp's: a failed
// scheduled scan followed by a good triggered run boots healthy.
func TestStartupSkipLogsTheBootVerdict(t *testing.T) {
	logs := captureLogs(t, slog.LevelInfo)
	dir := t.TempDir()
	schedule := scheduler.NewStamp(filepath.Join(dir, "last-run"))
	completed := scheduler.NewStamp(filepath.Join(dir, "last-completed"))
	if err := schedule.Record(false); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := completed.Record(true); err != nil {
		t.Fatalf("Record: %v", err)
	}
	marker := health.NewMarker(filepath.Join(dir, "healthy"))
	cfg := &config{Mode: modeBuiltin, Interval: time.Hour}

	bootDaemon(cfg, schedule, &reporter{completed: completed}, health.NewLatch(marker), time.Now())
	if !marker.CheckHealthy() {
		t.Fatal("marker boots unhealthy, want healthy from the good triggered run")
	}
	var line string
	for l := range strings.SplitSeq(logs.String(), "\n") {
		if strings.Contains(l, "startup scan skipped") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("no startup-skip line in:\n%s", logs.String())
	}
	if !strings.Contains(line, " healthy=true") {
		t.Errorf("startup-skip line %q does not carry healthy=true", line)
	}
	if strings.Contains(line, "=false") {
		t.Errorf("startup-skip line %q logs a failing verdict beside a healthy boot", line)
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
			trig: trigger.External, ran: true,
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
				health:   health.NewLatch(marker),
				reporter: testReporter(t, marker),
				stamp:    scheduler.NewStamp(filepath.Join(dir, "last-run")),
			}

			rep := jobReport{trigger: tt.trig, ran: tt.ran, err: tt.runErr, result: resultSuccess}
			if tt.runErr != nil {
				rep.result = resultExecError
			}
			d.recordOutcome(tt.ctxErr, &rep)

			if got := marker.Healthy(); got != tt.wantHealthy {
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
	logs := captureLogs(t, slog.LevelInfo)

	dir := t.TempDir()
	marker := health.NewMarker(filepath.Join(dir, "healthy"))
	d := &daemon{
		health:   health.NewLatch(marker),
		reporter: testReporter(t, marker),
		stamp:    scheduler.NewStamp(filepath.Join(dir, "missing-dir", "last-run")),
	}

	d.recordOutcome(nil, &jobReport{trigger: triggerInterval, ran: true, result: resultSuccess})

	if !marker.Healthy() {
		t.Error("marker healthy = false after a stamp write failure, want true (the failure must not demote the run)")
	}
	got := logs.String()
	if !strings.Contains(got, "level=WARN") || !strings.Contains(got, "cannot record the scan outcome") {
		t.Errorf("stamp write failure logged %q, want a WARN carrying \"cannot record the scan outcome\"", got)
	}
}
