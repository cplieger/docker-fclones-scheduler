package main

import (
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/scheduler/v4/trigger"
)

// startTestDaemon binds a trigger server on a per-test socket with a fake
// executor that finishes every request with out. It returns the socket path.
func startTestDaemon(t *testing.T, out trigger.Outcome) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "trigger.sock")

	ln, err := trigger.Listen(sock)
	if err != nil {
		t.Fatalf("trigger.Listen: %v", err)
	}
	q := trigger.NewQueue[struct{}](queueCapacity)
	srv := &trigger.Server[struct{}]{Queue: q}
	srv.Serve(ln)
	go func() {
		for j := range q.Jobs() {
			j.Start()
			j.Finish(out)
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		q.Close()
	})
	return sock
}

// The client tests exercise the real wire round-trip: runClient dials the
// socket, sends the request frame, streams events, and maps the final
// outcome to its exit code. runClient installs the process-global logger,
// so these run serially (no t.Parallel).
func TestRunClient_SuccessExitsZero(t *testing.T) {
	sock := startTestDaemon(t, trigger.Outcome{OK: true, Duration: time.Millisecond})
	if code := runClient(sock); code != 0 {
		t.Errorf("runClient(success) = %d, want 0", code)
	}
}

func TestRunClient_LockSkipExitsZeroWithReason(t *testing.T) {
	sock := startTestDaemon(t, trigger.Outcome{OK: true, Reason: "skipped: scan lock held by another process sharing /cache"})
	if code := runClient(sock); code != 0 {
		t.Errorf("runClient(lock skip) = %d, want 0 (documented overlap tolerance)", code)
	}
}

func TestRunClient_FailureExitsOne(t *testing.T) {
	sock := startTestDaemon(t, trigger.Outcome{OK: false, Duration: time.Millisecond})
	if code := runClient(sock); code != 1 {
		t.Errorf("runClient(failed run) = %d, want 1", code)
	}
}

func TestRunClient_NoDaemonExitsOne(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "absent.sock")
	if code := runClient(sock); code != 1 {
		t.Errorf("runClient(no daemon) = %d, want 1", code)
	}
}

func TestRunClient_QueueClosedExitsOne(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "trigger.sock")
	ln, err := trigger.Listen(sock)
	if err != nil {
		t.Fatalf("trigger.Listen: %v", err)
	}
	q := trigger.NewQueue[struct{}](queueCapacity)
	q.Close() // shutting down: submissions rejected with a reason
	srv := &trigger.Server[struct{}]{Queue: q}
	srv.Serve(ln)
	t.Cleanup(func() { _ = ln.Close() })

	if code := runClient(sock); code != 1 {
		t.Errorf("runClient(queue closed) = %d, want 1 (cancelled/rejected requests fail the trigger)", code)
	}
}

// The `reason` attribute is the whole operator-facing explanation of a
// triggered scan's outcome: an Ofelia job log shows the exit code, and the
// container log stream shows this line. A skipped run (the documented
// cross-container lock tolerance) exits 0 and must say why, and a failed run
// must carry either the daemon's own reason or the fallback pointing at the
// scan's own output. These swap the process-global logger, so they run
// serially (no t.Parallel).
func TestFinishResult_LogsTheOutcomeReason(t *testing.T) {
	tests := []struct {
		name       string
		ev         trigger.Event
		wantCode   int
		wantReason string
	}{
		{
			name:       "lock skip exits zero and names the skip",
			ev:         trigger.Event{OK: true, DurationMs: 3, Reason: "skipped: scan lock held by another process sharing /cache"},
			wantCode:   0,
			wantReason: "skipped: scan lock held by another process sharing /cache",
		},
		{
			name:       "failure keeps the daemon's own reason",
			ev:         trigger.Event{OK: false, DurationMs: 12, Reason: "scan timeout exceeded after 30s"},
			wantCode:   1,
			wantReason: "scan timeout exceeded after 30s",
		},
		{
			name:       "failure without a reason falls back to the log-stream pointer",
			ev:         trigger.Event{OK: false, DurationMs: 12},
			wantCode:   1,
			wantReason: "the run failed (see the container log stream)",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			orig := slog.Default()
			t.Cleanup(func() { slog.SetDefault(orig) })
			var logs strings.Builder
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})))

			if got := finishResult(tc.ev); got != tc.wantCode {
				t.Errorf("finishResult(%+v) = %d, want %d", tc.ev, got, tc.wantCode)
			}
			want := `reason="` + tc.wantReason + `"`
			if got := logs.String(); !strings.Contains(got, want) {
				t.Errorf("finishResult(%+v) logged %q, want it to carry %s", tc.ev, got, want)
			}
		})
	}
}

// A run that succeeded with nothing to explain carries no reason attribute at
// all, so `reason` present in Loki always means the daemon had something to
// say about the outcome.
func TestFinishResult_SilentSuccessLogsNoReasonAttr(t *testing.T) {
	orig := slog.Default()
	t.Cleanup(func() { slog.SetDefault(orig) })
	var logs strings.Builder
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})))

	ev := trigger.Event{OK: true, DurationMs: 3}
	if got := finishResult(ev); got != 0 {
		t.Errorf("finishResult(%+v) = %d, want 0", ev, got)
	}
	if got := logs.String(); strings.Contains(got, "reason=") {
		t.Errorf("finishResult(%+v) logged %q, want no reason attribute", ev, got)
	}
}
