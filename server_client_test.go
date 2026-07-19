package main

import (
	"path/filepath"
	"testing"
	"time"
)

// startTestDaemon binds a trigger server on a per-test socket with a fake
// executor that finishes every request with out. It returns the socket path.
func startTestDaemon(t *testing.T, out runOutcome) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "trigger.sock")

	ln, err := listenTrigger(sock)
	if err != nil {
		t.Fatalf("listenTrigger: %v", err)
	}
	q := newRunQueue(queueCapacity)
	srv := &triggerServer{queue: q}
	go srv.serve(ln)
	go func() {
		for r := range q.requests {
			close(r.started)
			r.finish(out)
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		q.close()
	})
	return sock
}

// The client tests exercise the real wire round-trip: runClient dials the
// socket, sends the request frame, streams events, and maps the final
// outcome to its exit code. runClient installs the process-global logger,
// so these run serially (no t.Parallel).
func TestRunClient_SuccessExitsZero(t *testing.T) {
	sock := startTestDaemon(t, runOutcome{ok: true, duration: time.Millisecond})
	if code := runClient(sock); code != 0 {
		t.Errorf("runClient(success) = %d, want 0", code)
	}
}

func TestRunClient_LockSkipExitsZeroWithReason(t *testing.T) {
	sock := startTestDaemon(t, runOutcome{ok: true, reason: "skipped: scan lock held by another process sharing /cache"})
	if code := runClient(sock); code != 0 {
		t.Errorf("runClient(lock skip) = %d, want 0 (documented overlap tolerance)", code)
	}
}

func TestRunClient_FailureExitsOne(t *testing.T) {
	sock := startTestDaemon(t, runOutcome{ok: false, duration: time.Millisecond})
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
	ln, err := listenTrigger(sock)
	if err != nil {
		t.Fatalf("listenTrigger: %v", err)
	}
	q := newRunQueue(queueCapacity)
	q.close() // shutting down: submissions rejected with a reason
	srv := &triggerServer{queue: q}
	go srv.serve(ln)
	t.Cleanup(func() { _ = ln.Close() })

	if code := runClient(sock); code != 1 {
		t.Errorf("runClient(queue closed) = %d, want 1 (cancelled/rejected requests fail the trigger)", code)
	}
}
