package capbuf_test

import (
	"testing"

	"github.com/cplieger/docker-fclones-scheduler/internal/capbuf"
	"pgregory.net/rapid"
)

func TestBufferWritesBelowMaxArePreserved(t *testing.T) {
	t.Parallel()
	lb := &capbuf.Buffer{Max: 10}

	n, err := lb.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("Write: unexpected error: %v", err)
	}
	if n != 5 {
		t.Errorf("Write returned n=%d, want 5", n)
	}
	if got := lb.String(); got != "hello" {
		t.Errorf("String() = %q, want %q", got, "hello")
	}
}

func TestBufferTruncatesWriteThatCrossesMax(t *testing.T) {
	t.Parallel()
	lb := &capbuf.Buffer{Max: 5}

	n, err := lb.Write([]byte("hello world"))
	if err != nil {
		t.Fatalf("Write: unexpected error: %v", err)
	}
	if n != 11 {
		t.Errorf("Write returned n=%d, want 11 (full input length)", n)
	}
	if got := lb.String(); got != "hello" {
		t.Errorf("String() = %q, want %q (truncated to max)", got, "hello")
	}
}

func TestBufferDiscardsWritesAfterMaxReached(t *testing.T) {
	t.Parallel()
	lb := &capbuf.Buffer{Max: 5}

	if _, err := lb.Write([]byte("hello")); err != nil {
		t.Fatalf("first Write: %v", err)
	}

	n, err := lb.Write([]byte(" world"))
	if err != nil {
		t.Fatalf("second Write: %v", err)
	}
	if n != 6 {
		t.Errorf("second Write returned n=%d, want 6 (full input length)", n)
	}
	if got := lb.String(); got != "hello" {
		t.Errorf("String() = %q, want %q (buffer frozen at max)", got, "hello")
	}
}

func TestBufferZeroMaxDiscardsEverything(t *testing.T) {
	t.Parallel()
	lb := &capbuf.Buffer{Max: 0}

	n, err := lb.Write([]byte("anything"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != 8 {
		t.Errorf("Write returned n=%d, want 8", n)
	}
	if got := lb.String(); got != "" {
		t.Errorf("String() = %q, want empty", got)
	}
}

func TestBufferEmptyWriteIsNoop(t *testing.T) {
	t.Parallel()
	lb := &capbuf.Buffer{Max: 5}

	n, err := lb.Write(nil)
	if err != nil {
		t.Fatalf("Write(nil): %v", err)
	}
	if n != 0 {
		t.Errorf("Write(nil) returned n=%d, want 0", n)
	}

	n, err = lb.Write([]byte{})
	if err != nil {
		t.Fatalf("Write(empty): %v", err)
	}
	if n != 0 {
		t.Errorf("Write(empty) returned n=%d, want 0", n)
	}
	if got := lb.String(); got != "" {
		t.Errorf("String() = %q, want empty", got)
	}
}

func TestBufferExactBoundaryFill(t *testing.T) {
	t.Parallel()
	lb := &capbuf.Buffer{Max: 5}

	n, err := lb.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != 5 {
		t.Errorf("Write returned n=%d, want 5", n)
	}
	if got := lb.String(); got != "hello" {
		t.Errorf("String() = %q, want %q", got, "hello")
	}

	n, err = lb.Write([]byte{})
	if err != nil {
		t.Fatalf("zero-length Write after fill: %v", err)
	}
	if n != 0 {
		t.Errorf("zero-length Write returned n=%d, want 0", n)
	}
}

func TestBufferTotalTracksAllBytes(t *testing.T) {
	t.Parallel()
	lb := &capbuf.Buffer{Max: 5}

	if _, err := lb.Write([]byte("hello world")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got, want := lb.Total(), 11; got != want {
		t.Errorf("Total() = %d, want %d", got, want)
	}
	if !lb.Truncated() {
		t.Errorf("Truncated() = false, want true after overflow")
	}
	if got := lb.String(); got != "hello" {
		t.Errorf("String() = %q, want %q", got, "hello")
	}
}

func TestBufferNotTruncatedBelowMax(t *testing.T) {
	t.Parallel()
	lb := &capbuf.Buffer{Max: 10}

	if _, err := lb.Write([]byte("hi")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if lb.Truncated() {
		t.Errorf("Truncated() = true, want false when under max")
	}
	if got, want := lb.Total(), 2; got != want {
		t.Errorf("Total() = %d, want %d", got, want)
	}
}

// TestBufferWriteFillsExactRemaining locks the exact-fill boundary: a
// Write whose length equals the remaining room must store all of it, bringing
// the buffer to exactly Max with nothing discarded.
func TestBufferWriteFillsExactRemaining(t *testing.T) {
	t.Parallel()
	lb := &capbuf.Buffer{Max: 10}

	// Partially fill so the remaining room is a non-trivial 7 bytes.
	if n, err := lb.Write([]byte("abc")); err != nil || n != 3 {
		t.Fatalf("setup Write(%q) = (%d, %v), want (3, nil)", "abc", n, err)
	}

	// Write exactly the remaining room (10 - 3 = 7) bytes.
	n, err := lb.Write([]byte("defghij"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != 7 {
		t.Errorf("Write returned n=%d, want 7", n)
	}
	if got := lb.String(); got != "abcdefghij" {
		t.Errorf("String() = %q, want %q (filled to exactly Max)", got, "abcdefghij")
	}
	if lb.Truncated() {
		t.Errorf("Truncated() = true, want false (total 10 == Max, nothing discarded)")
	}
}

// TestBufferWriteAfterMaxLoweredStoresNothing locks the negative-room
// boundary: lowering Max below the current buffer length makes the available
// room negative, which must clamp to zero so a further Write stores nothing
// without panicking.
func TestBufferWriteAfterMaxLoweredStoresNothing(t *testing.T) {
	t.Parallel()
	lb := &capbuf.Buffer{Max: 10}

	if n, err := lb.Write([]byte("0123456789")); err != nil || n != 10 {
		t.Fatalf("setup Write = (%d, %v), want (10, nil)", n, err)
	}

	// Lower Max below the current length: Max - buf.Len() is now negative.
	lb.Max = 4

	n, err := lb.Write([]byte("more"))
	if err != nil {
		t.Fatalf("Write after Max lowered: %v", err)
	}
	if n != 4 {
		t.Errorf("Write after Max lowered n=%d, want 4 (full input length)", n)
	}
	if got := lb.String(); got != "0123456789" {
		t.Errorf("String() = %q, want %q (frozen; negative room stores nothing)", got, "0123456789")
	}
	if got, want := lb.Total(), 14; got != want {
		t.Errorf("Total() = %d, want %d (10 stored + 4 discarded)", got, want)
	}
	if !lb.Truncated() {
		t.Errorf("Truncated() = false, want true (total 14 exceeds lowered Max 4)")
	}
}

func TestProperty_BufferInvariants(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		maxVal := rapid.IntRange(0, 256).Draw(rt, "max")
		numWrites := rapid.IntRange(0, 20).Draw(rt, "numWrites")

		lb := &capbuf.Buffer{Max: maxVal}
		totalInput := 0
		totalReturned := 0

		for i := range numWrites {
			payload := rapid.SliceOfN(rapid.Byte(), 0, 64).Draw(rt, "payload")
			totalInput += len(payload)

			n, err := lb.Write(payload)
			if err != nil {
				rt.Fatalf("Write(len=%d): %v", len(payload), err)
			}
			if n != len(payload) {
				rt.Fatalf("Write returned n=%d, want %d", n, len(payload))
			}
			totalReturned += n

			if got := len([]byte(lb.String())); got > maxVal {
				rt.Fatalf("buffer length %d exceeds max %d after write %d", got, maxVal, i)
			}
		}

		if totalReturned != totalInput {
			rt.Fatalf("sum of Write returns %d != sum of input lengths %d",
				totalReturned, totalInput)
		}

		if got := lb.Total(); got != totalInput {
			rt.Fatalf("Total() = %d, want sum of input lengths %d", got, totalInput)
		}
		if got, want := lb.Truncated(), totalInput > maxVal; got != want {
			rt.Fatalf("Truncated() = %v, want %v (total=%d max=%d)",
				got, want, totalInput, maxVal)
		}
	})
}

func TestProperty_BufferStringIsStable(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		maxVal := rapid.IntRange(0, 128).Draw(rt, "max")
		payload := rapid.SliceOfN(rapid.Byte(), 0, 200).Draw(rt, "payload")

		lb := &capbuf.Buffer{Max: maxVal}
		_, _ = lb.Write(payload)

		first := lb.String()
		second := lb.String()
		third := lb.String()

		if first != second || second != third {
			rt.Fatalf("String() not stable: %q / %q / %q", first, second, third)
		}
	})
}

// TestProperty_BufferRetainsInputPrefix is the content oracle the other
// Buffer property lacks. Across an arbitrary sequence of writes the
// buffer must retain exactly the first min(total, Max) bytes of the
// concatenated input, because Write greedily fills the remaining room with the
// earliest bytes and drops the rest. TestProperty_BufferInvariants
// asserts only length, Total, and Truncated, so a content- or
// accumulation-corrupting mutant -- one that stores the tail instead of the
// head, or resets the buffer on each write -- keeps those invariants and
// survives it; this pins the retained bytes themselves.
func TestProperty_BufferRetainsInputPrefix(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		maxVal := rapid.IntRange(0, 256).Draw(rt, "max")
		numWrites := rapid.IntRange(0, 20).Draw(rt, "numWrites")

		lb := &capbuf.Buffer{Max: maxVal}
		var concat []byte
		for range numWrites {
			payload := rapid.SliceOfN(rapid.Byte(), 0, 64).Draw(rt, "payload")
			concat = append(concat, payload...)
			if _, err := lb.Write(payload); err != nil {
				rt.Fatalf("Write(len=%d): %v", len(payload), err)
			}
		}

		want := concat[:min(len(concat), maxVal)]
		if got := lb.String(); got != string(want) {
			rt.Fatalf("String() = %q, want %q (first %d bytes of the %d-byte concatenated input)",
				got, want, len(want), len(concat))
		}
	})
}
