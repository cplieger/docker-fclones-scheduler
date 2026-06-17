package ioutil

// Boundary-characterization tests for the two surviving CONDITIONALS_BOUNDARY
// gremlins mutants in LimitedBuffer.Write:
//
//   ioutil.go:96  `lb.buf.Len() >= lb.Max`  ->  `>`
//   ioutil.go:100 `len(p) > remaining`      ->  `>=`
//
// Both are EQUIVALENT mutants; these tests pin the exact boundary contract so a
// future change to the buffer's invariant would be caught, but they do not (and
// cannot) "kill" the mutants — see the per-test reasoning. Internal test package
// so the analysis lives beside the code under test.

import "testing"

// gk_docker_fclones_scheduler_u1_assertWrite is a focused assertion helper for
// LimitedBuffer.Write outcomes (return n + post-write String()).
func gk_docker_fclones_scheduler_u1_assertWrite(t *testing.T, lb *LimitedBuffer, p []byte, wantN int, wantStr string) {
	t.Helper()
	n, err := lb.Write(p)
	if err != nil {
		t.Fatalf("Write(%q) returned error %v, want nil", p, err)
	}
	if n != wantN {
		t.Errorf("Write(%q) n = %d, want %d", p, n, wantN)
	}
	if got := lb.String(); got != wantStr {
		t.Errorf("after Write(%q), String() = %q, want %q", p, got, wantStr)
	}
}

// TestGkDockerFclonesSchedulerU1_WriteAtCapBoundaryDiscards pins ioutil.go:96.
//
// Once buf.Len() == Max, a further non-empty Write must be fully discarded
// (buffer frozen, returns full input length, Total keeps counting, Truncated
// flips true). The original `>= Max` early-returns here. The mutated `> Max` is
// false (buf.Len() never exceeds Max), so it falls through with remaining == 0
// and writes p[:0] (nothing) -- yielding the identical frozen buffer and return
// value. Hence the mutant is EQUIVALENT: this test passes under both operators
// and only locks the at-cap discard contract.
func TestGkDockerFclonesSchedulerU1_WriteAtCapBoundaryDiscards(t *testing.T) {
	t.Parallel()
	lb := &LimitedBuffer{Max: 8}

	// Fill the buffer to exactly Max (buf.Len() == Max boundary).
	gk_docker_fclones_scheduler_u1_assertWrite(t, lb, []byte("abcdefgh"), 8, "abcdefgh")
	if got := lb.String(); len(got) != 8 {
		t.Fatalf("setup: buffer length = %d, want 8 (== Max)", len(got))
	}

	// Now at the boundary: any further non-empty Write is discarded.
	gk_docker_fclones_scheduler_u1_assertWrite(t, lb, []byte("XYZ"), 3, "abcdefgh")

	if got, want := lb.Total(), 11; got != want {
		t.Errorf("Total() = %d, want %d (8 stored + 3 discarded)", got, want)
	}
	if !lb.Truncated() {
		t.Errorf("Truncated() = false, want true once input exceeds Max")
	}
}

// TestGkDockerFclonesSchedulerU1_WriteFillsExactRemaining pins ioutil.go:100.
//
// Into a partially-filled buffer (Len 3, Max 10 => remaining 7), a Write of
// exactly `remaining` (7) bytes must store all of them, bringing buf to exactly
// Max with nothing discarded. The original `len(p) > remaining` is false here
// and writes the full p; the mutated `len(p) >= remaining` is true and writes
// p[:remaining], but p[:7] == p when len(p) == 7 -- the same full p. Hence the
// mutant is EQUIVALENT: this test passes under both operators and only locks the
// exact-fill contract.
func TestGkDockerFclonesSchedulerU1_WriteFillsExactRemaining(t *testing.T) {
	t.Parallel()
	lb := &LimitedBuffer{Max: 10}

	// Partially fill so remaining is a non-trivial 7.
	gk_docker_fclones_scheduler_u1_assertWrite(t, lb, []byte("abc"), 3, "abc")

	// Write exactly `remaining` (10 - 3 = 7) bytes: the len(p) == remaining edge.
	gk_docker_fclones_scheduler_u1_assertWrite(t, lb, []byte("defghij"), 7, "abcdefghij")

	if got := lb.String(); len(got) != 10 {
		t.Errorf("buffer length = %d, want 10 (filled to exactly Max)", len(got))
	}
	if got, want := lb.Total(), 10; got != want {
		t.Errorf("Total() = %d, want %d", got, want)
	}
	if lb.Truncated() {
		t.Errorf("Truncated() = true, want false (total 10 == Max, nothing discarded)")
	}
}
