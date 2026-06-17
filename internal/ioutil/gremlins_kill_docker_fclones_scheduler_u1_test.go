package ioutil

// Behavioral-contract tests for the boundary cases of LimitedBuffer.Write.
//
// Round 1 found two surviving CONDITIONALS_BOUNDARY mutants here
// (`lb.buf.Len() >= lb.Max` and `len(p) > remaining`); both were EQUIVALENT.
// Round 2 ELIMINATED them by rewriting Write as a `min`/`max` clamp (see
// ioutil.go) -- the two mutable comparisons are gone (both now live inside the
// min/max builtins, which CONDITIONALS_BOUNDARY does not mutate). These tests
// remain as the at-cap and exact-fill characterization that locks the rewrite's
// contract. Internal test package so the analysis lives beside the code under
// test.

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

// TestGkDockerFclonesSchedulerU1_WriteAtCapBoundaryDiscards locks the at-cap
// path. Once buf.Len() == Max, a further non-empty Write must be fully
// discarded -- buffer frozen, returns the full input length, Total keeps
// counting, Truncated flips true. After the round-2 rewrite this is the
// room == 0 case where `min(len(p), room)` is 0, so nothing is stored.
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

// TestGkDockerFclonesSchedulerU1_WriteFillsExactRemaining locks the exact-fill
// path. Into a partially-filled buffer (Len 3, Max 10 => remaining 7), a Write
// of exactly `remaining` (7) bytes must store all of them, bringing buf to
// exactly Max with nothing discarded. After the round-2 rewrite this is the
// case where room >= len(p), so `min(len(p), room)` == len(p) and the whole
// input is stored.
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
