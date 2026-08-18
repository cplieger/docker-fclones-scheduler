package main

import (
	"path/filepath"
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
