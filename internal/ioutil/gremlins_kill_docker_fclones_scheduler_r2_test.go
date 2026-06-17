package ioutil

// Round-2 regression guard for the LimitedBuffer.Write rewrite (ioutil.go),
// which replaced the hand-rolled `>= Max` / `> remaining` branches with
//
//	room := max(0, lb.Max-lb.buf.Len())
//	lb.buf.Write(p[:min(len(p), room)])
//
// The `max(0, ...)` is load-bearing: it covers an edge the pre-rewrite
// `Len() >= Max` early-return also handled but the existing suite never
// exercised -- Max being lowered below the current buffer length after writes,
// which makes `Max - Len()` negative. Without the clamp, `min(len(p), room)`
// would be negative and `p[:negative]` would panic. This test pins the
// no-panic, store-nothing contract so a later "the clamp is redundant, room is
// always >= 0" simplification is caught.

import "testing"

func TestGkFclonesR2_WriteAfterMaxLoweredStoresNothing(t *testing.T) {
	t.Parallel()

	lb := &LimitedBuffer{Max: 10}
	if n, err := lb.Write([]byte("0123456789")); err != nil || n != 10 {
		t.Fatalf("setup Write = (%d, %v), want (10, nil)", n, err)
	}

	// Lower Max below the current length: Max - Len() is now negative.
	lb.Max = 4

	n, err := lb.Write([]byte("more"))
	if err != nil {
		t.Fatalf("Write after Max lowered returned error %v, want nil", err)
	}
	if n != 4 {
		t.Errorf("Write after Max lowered n = %d, want 4 (full input length)", n)
	}
	if got := lb.String(); got != "0123456789" {
		t.Errorf("buffer = %q, want %q (frozen; negative room stores nothing)", got, "0123456789")
	}
}
