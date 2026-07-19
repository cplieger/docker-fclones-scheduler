package main

import (
	"errors"
	"testing"
)

func TestRunQueue_FIFOAndExactlyOneResult(t *testing.T) {
	t.Parallel()
	q := newRunQueue(4)

	first := newRequest("external")
	second := newRequest("interval")
	if err := q.submit(first); err != nil {
		t.Fatalf("submit(first): %v", err)
	}
	if err := q.submit(second); err != nil {
		t.Fatalf("submit(second): %v", err)
	}

	if got := <-q.requests; got != first {
		t.Fatal("queue did not deliver the first request first")
	}
	if got := <-q.requests; got != second {
		t.Fatal("queue did not deliver the second request second")
	}

	// Exactly one buffered result per request: finish never blocks, and the
	// waiter receives the delivered outcome.
	first.finish(runOutcome{ok: true})
	if out := <-first.result; !out.ok {
		t.Error("first request's delivered result lost or altered")
	}
}

func TestRunQueue_FullRejectsImmediately(t *testing.T) {
	t.Parallel()
	q := newRunQueue(1)
	if err := q.submit(newRequest("external")); err != nil {
		t.Fatalf("submit into empty queue: %v", err)
	}
	err := q.submit(newRequest("external"))
	if !errors.Is(err, errQueueFull) {
		t.Fatalf("submit into full queue = %v, want errQueueFull", err)
	}
}

func TestRunQueue_ClosedRejectsAndDrains(t *testing.T) {
	t.Parallel()
	q := newRunQueue(2)
	queued := newRequest("external")
	if err := q.submit(queued); err != nil {
		t.Fatalf("submit: %v", err)
	}

	q.close()
	q.close() // idempotent

	if err := q.submit(newRequest("external")); !errors.Is(err, errQueueClosed) {
		t.Fatalf("submit after close = %v, want errQueueClosed", err)
	}

	// The already-queued request drains through the closed channel, then the
	// range terminates.
	var drained []*request
	for r := range q.requests {
		drained = append(drained, r)
	}
	if len(drained) != 1 || drained[0] != queued {
		t.Fatalf("drained %d requests, want the 1 queued before close", len(drained))
	}
}
