package ioutil_test

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/fclones-wrapper/internal/ioutil"
	"pgregory.net/rapid"
)

func TestLimitedBufferWritesBelowMaxArePreserved(t *testing.T) {
	t.Parallel()
	lb := &ioutil.LimitedBuffer{Max: 10}

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

func TestLimitedBufferTruncatesWriteThatCrossesMax(t *testing.T) {
	t.Parallel()
	lb := &ioutil.LimitedBuffer{Max: 5}

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

func TestLimitedBufferDiscardsWritesAfterMaxReached(t *testing.T) {
	t.Parallel()
	lb := &ioutil.LimitedBuffer{Max: 5}

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

func TestLimitedBufferZeroMaxDiscardsEverything(t *testing.T) {
	t.Parallel()
	lb := &ioutil.LimitedBuffer{Max: 0}

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

func TestLimitedBufferEmptyWriteIsNoop(t *testing.T) {
	t.Parallel()
	lb := &ioutil.LimitedBuffer{Max: 5}

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

func TestLimitedBufferExactBoundaryFill(t *testing.T) {
	t.Parallel()
	lb := &ioutil.LimitedBuffer{Max: 5}

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

func TestLimitedBufferTotalTracksAllBytes(t *testing.T) {
	t.Parallel()
	lb := &ioutil.LimitedBuffer{Max: 5}

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

func TestLimitedBufferNotTruncatedBelowMax(t *testing.T) {
	t.Parallel()
	lb := &ioutil.LimitedBuffer{Max: 10}

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

// TestLimitedBufferWriteFillsExactRemaining locks the exact-fill boundary: a
// Write whose length equals the remaining room must store all of it, bringing
// the buffer to exactly Max with nothing discarded.
func TestLimitedBufferWriteFillsExactRemaining(t *testing.T) {
	t.Parallel()
	lb := &ioutil.LimitedBuffer{Max: 10}

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

// TestLimitedBufferWriteAfterMaxLoweredStoresNothing locks the negative-room
// boundary: lowering Max below the current buffer length makes the available
// room negative, which must clamp to zero so a further Write stores nothing
// without panicking.
func TestLimitedBufferWriteAfterMaxLoweredStoresNothing(t *testing.T) {
	t.Parallel()
	lb := &ioutil.LimitedBuffer{Max: 10}

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

func TestProperty_LimitedBufferInvariants(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		maxVal := rapid.IntRange(0, 256).Draw(rt, "max")
		numWrites := rapid.IntRange(0, 20).Draw(rt, "numWrites")

		lb := &ioutil.LimitedBuffer{Max: maxVal}
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

func TestProperty_LimitedBufferStringIsStable(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		maxVal := rapid.IntRange(0, 128).Draw(rt, "max")
		payload := rapid.SliceOfN(rapid.Byte(), 0, 200).Draw(rt, "payload")

		lb := &ioutil.LimitedBuffer{Max: maxVal}
		_, _ = lb.Write(payload)

		first := lb.String()
		second := lb.String()
		third := lb.String()

		if first != second || second != third {
			rt.Fatalf("String() not stable: %q / %q / %q", first, second, third)
		}
	})
}

// --- Tests: filteringWriter ---

func TestFilteringWriterDropsFilteredLines(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	fw := ioutil.NewFilteringWriter(&out)

	input := "[ts] fclones:  info: Started grouping\n" +
		"keep this warn line\n" +
		"[ts] fclones:  info: Scanned 100 file entries\n" +
		"keep this too\n"
	if _, err := fw.Write([]byte(input)); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got := out.String()
	want := "keep this warn line\nkeep this too\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFilteringWriterHandlesPartialLines(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	fw := ioutil.NewFilteringWriter(&out)

	fw.Write([]byte("[ts] fclones:  info: Start"))
	fw.Write([]byte("ed grouping\nkeep this\n"))

	got := out.String()
	if got != "keep this\n" {
		t.Errorf("got %q, want %q", got, "keep this\n")
	}
}

// --- Tests: ReadFileWithLimit ---

func TestReadFileWithLimit(t *testing.T) {
	t.Parallel()
	t.Run("reads file within limit", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "small.txt")
		os.WriteFile(path, []byte("hello"), 0o644)

		data, err := ioutil.ReadFileWithLimit(path, 100)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(data) != "hello" {
			t.Errorf("got %q, want %q", data, "hello")
		}
	})

	t.Run("rejects file over limit", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "big.txt")
		os.WriteFile(path, make([]byte, 200), 0o644)

		_, err := ioutil.ReadFileWithLimit(path, 100)
		if err == nil {
			t.Error("expected error for oversized file")
		}
	})

	t.Run("returns error for missing file", func(t *testing.T) {
		t.Parallel()
		_, err := ioutil.ReadFileWithLimit("/nonexistent/file.txt", 100)
		if err == nil {
			t.Error("expected error for missing file")
		}
	})
}

func TestReadFileWithLimitExactBoundary(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "exact.txt")
	data := []byte("12345")
	os.WriteFile(path, data, 0o644)

	got, err := ioutil.ReadFileWithLimit(path, int64(len(data)))
	if err != nil {
		t.Fatalf("unexpected error at exact boundary: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("got %q, want %q", got, data)
	}

	_, err = ioutil.ReadFileWithLimit(path, int64(len(data)-1))
	if err == nil {
		t.Error("expected error when file exceeds limit by 1 byte")
	}
}

func TestReadFileWithLimitEmptyFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "empty.txt")
	os.WriteFile(path, []byte{}, 0o644)

	got, err := ioutil.ReadFileWithLimit(path, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %d bytes", len(got))
	}
}

func TestReadFileWithLimitZeroLimit(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "nonempty.txt")
	os.WriteFile(path, []byte("x"), 0o644)

	_, err := ioutil.ReadFileWithLimit(path, 0)
	if err == nil {
		t.Error("expected error for 1-byte file with 0-byte limit")
	}
}

func TestReadFileWithLimitErrorsOnUnreadableDirectory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	data, err := ioutil.ReadFileWithLimit(dir, 1<<30)
	if err == nil {
		t.Fatal("ReadFileWithLimit(<dir>, 1GiB) = nil error, want a read error for a directory")
	}
	if data != nil {
		t.Errorf("ReadFileWithLimit(<dir>) returned %d bytes with the error, want nil data", len(data))
	}
}

func TestReadFileWithLimitEmptyFileZeroLimit(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "empty.txt")
	os.WriteFile(path, []byte{}, 0o644)

	got, err := ioutil.ReadFileWithLimit(path, 0)
	if err != nil {
		t.Fatalf("ReadFileWithLimit empty file with 0 limit: unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %d bytes", len(got))
	}
}

func TestReadFileWithLimitDetectsGrowthDuringRead(t *testing.T) {
	t.Parallel()

	if _, statErr := os.Stat("/dev/zero"); statErr != nil {
		t.Skip("/dev/zero not available on this platform")
	}

	data, err := ioutil.ReadFileWithLimit("/dev/zero", 100)
	if err == nil {
		t.Fatal("ReadFileWithLimit(/dev/zero, 100) = nil error, want a growth-detection error")
	}
	if data != nil {
		t.Errorf("ReadFileWithLimit(/dev/zero, 100) returned %d bytes with the error, want nil data", len(data))
	}
}

func TestReadFileWithLimitMaxInt64DoesNotWrap(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "small.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// At maxBytes == math.MaxInt64 the guard must NOT do limit++ (that would
	// wrap to a negative LimitReader bound, which returns immediate EOF and
	// silently yields empty data). No real file can exceed MaxInt64, so the
	// full content must come back verbatim.
	data, err := ioutil.ReadFileWithLimit(path, math.MaxInt64)
	if err != nil {
		t.Fatalf("ReadFileWithLimit(path, MaxInt64) = err %v, want nil", err)
	}
	if string(data) != "hello" {
		t.Errorf("ReadFileWithLimit(path, MaxInt64) = %q, want %q (overflow guard must not truncate to empty)", data, "hello")
	}
}

func TestReadFileWithLimitNearMaxInt64Boundary(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "small.txt")
	content := []byte("0123456789")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// MaxInt64-1 takes the limit++ branch (limit becomes MaxInt64); the read
	// must still succeed and return the file verbatim.
	data, err := ioutil.ReadFileWithLimit(path, math.MaxInt64-1)
	if err != nil {
		t.Fatalf("ReadFileWithLimit(path, MaxInt64-1) = err %v, want nil", err)
	}
	if string(data) != string(content) {
		t.Errorf("ReadFileWithLimit(path, MaxInt64-1) = %q, want %q", data, content)
	}
}

func TestReadFileWithLimitNegativeLimitRejected(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "data.txt")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// A negative limit can never be satisfied: info.Size() (>=0) always
	// exceeds it, so the Stat check rejects before any read. This path is
	// unreachable by FuzzReadFileWithLimit (its limit mask forces >= 0).
	data, err := ioutil.ReadFileWithLimit(path, -1)
	if err == nil {
		t.Fatal("ReadFileWithLimit(1-byte file, -1) = nil error, want a limit-exceeded error")
	}
	if data != nil {
		t.Errorf("ReadFileWithLimit(path, -1) returned %d bytes with the error, want nil data", len(data))
	}
}

// TestFilteringWriterWritesLeadingBlankLine guards the newline-search boundary
// in Write: a buffer whose very first byte is '\n' (idx == 0) must still be
// recognised as a complete line and forwarded, not buffered. Mutating the
// `idx < 0` no-newline guard to `idx <= 0` would treat a leading newline as
// "no newline found", swallowing the entire write.
func TestFilteringWriterWritesLeadingBlankLine(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	fw := ioutil.NewFilteringWriter(&out)

	if _, err := fw.Write([]byte("\nkeep this\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got := out.String()
	want := "\nkeep this\n"
	if got != want {
		t.Errorf("Write(%q) wrote %q, want %q", "\nkeep this\n", got, want)
	}
}

func TestFilteringWriterFlushEmitsBufferedPartialLine(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	fw := ioutil.NewFilteringWriter(&out)
	fw.Write([]byte("partial line without newline"))
	fw.Flush()
	if got := out.String(); got != "partial line without newline" {
		t.Errorf("after Flush, out = %q, want partial line", got)
	}
}

func TestFilteringWriterFlushDropsFilteredLine(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	fw := ioutil.NewFilteringWriter(&out)
	fw.Write([]byte("[ts] fclones:  info: Started grouping"))
	fw.Flush()
	if got := out.String(); got != "" {
		t.Errorf("after Flush of filtered line, out = %q, want empty", got)
	}
}

func TestFilteringWriterFlushEmptyIsNoop(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	fw := ioutil.NewFilteringWriter(&out)
	if err := fw.Flush(); err != nil {
		t.Fatalf("Flush on empty buffer: %v", err)
	}
}

func TestFilteringWriterCloseFlushesPartialLine(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	fw := ioutil.NewFilteringWriter(&out)
	fw.Write([]byte("tail line no newline"))
	fw.Close()
	if got := out.String(); got != "tail line no newline" {
		t.Errorf("after Close, out = %q, want tail line", got)
	}
}

func TestFilteringWriterFlushPropagatesSinkError(t *testing.T) {
	t.Parallel()

	sink := &errOnWrite{}
	fw := ioutil.NewFilteringWriter(sink)

	if _, err := fw.Write([]byte("buffered partial line, no newline")); err != nil {
		t.Fatalf("Write(partial) buffered the line but returned error %v, want nil", err)
	}

	err := fw.Flush()
	if err == nil {
		t.Fatal("Flush of a buffered non-filtered line into a failing sink = nil error, want the sink error")
	}
	if sink.calls != 1 {
		t.Errorf("sink.Write called %d times during Flush, want 1", sink.calls)
	}
}

type errOnWrite struct{ calls int }

func (e *errOnWrite) Write(p []byte) (int, error) {
	e.calls++
	return 0, os.ErrClosed
}

func TestFilteringWriterCapsUnboundedNoNewlineFlood(t *testing.T) {
	t.Parallel()

	const cap = 1 << 20 // mirrors maxLineBytes

	t.Run("flushes oversized non-filtered partial line and resets buffer", func(t *testing.T) {
		t.Parallel()
		var out bytes.Buffer
		fw := ioutil.NewFilteringWriter(&out)

		flood := bytes.Repeat([]byte("x"), cap+1)
		n, err := fw.Write(flood)
		if err != nil {
			t.Fatalf("Write(flood): unexpected error: %v", err)
		}
		if n != len(flood) {
			t.Errorf("Write(flood) = %d, want %d", n, len(flood))
		}
		if out.Len() != len(flood) {
			t.Errorf("sink got %d bytes, want %d (oversized line flushed at cap)", out.Len(), len(flood))
		}

		// Buffer was reset at the cap: a following newline-terminated line is
		// emitted on its own, not concatenated with the flushed flood.
		out.Reset()
		if _, err := fw.Write([]byte("next\n")); err != nil {
			t.Fatalf("Write(next): %v", err)
		}
		if got := out.String(); got != "next\n" {
			t.Errorf("after cap reset, sink = %q, want %q", got, "next\n")
		}
	})

	t.Run("drops oversized filtered partial line and resets buffer", func(t *testing.T) {
		t.Parallel()
		var out bytes.Buffer
		fw := ioutil.NewFilteringWriter(&out)

		// A >1MB no-newline run that contains a filtered pattern: the cap fires
		// and the line is dropped (not forwarded), buffer reset.
		flood := append([]byte("info: Scanned "), bytes.Repeat([]byte("9"), cap+1)...)
		if _, err := fw.Write(flood); err != nil {
			t.Fatalf("Write(filtered flood): %v", err)
		}
		if out.Len() != 0 {
			t.Errorf("sink got %d bytes, want 0 (filtered oversized line dropped)", out.Len())
		}

		out.Reset()
		if _, err := fw.Write([]byte("kept\n")); err != nil {
			t.Fatalf("Write(kept): %v", err)
		}
		if got := out.String(); got != "kept\n" {
			t.Errorf("after filtered-cap reset, sink = %q, want %q", got, "kept\n")
		}
	})

	t.Run("propagates sink error and resets buffer on oversized flush", func(t *testing.T) {
		t.Parallel()
		sink := &errOnWrite{}
		fw := ioutil.NewFilteringWriter(sink)

		flood := bytes.Repeat([]byte("x"), cap+1)
		n, err := fw.Write(flood)
		if err == nil {
			t.Fatal("Write(flood) into failing sink: want error, got nil")
		}
		if n != len(flood) {
			t.Errorf("Write(flood) = %d, want %d even on sink error", n, len(flood))
		}
		if sink.calls != 1 {
			t.Errorf("sink.Write called %d times, want 1", sink.calls)
		}
	})
}

func TestFilteringWriterCapFiresAcrossMultipleWrites(t *testing.T) {
	t.Parallel()
	const maxLine = 1 << 20
	var out bytes.Buffer
	fw := ioutil.NewFilteringWriter(&out)
	half := bytes.Repeat([]byte("y"), maxLine/2+1)
	if _, err := fw.Write(half); err != nil {
		t.Fatalf("Write(half #1): %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("after first sub-cap write, sink = %d, want 0", out.Len())
	}
	if _, err := fw.Write(half); err != nil {
		t.Fatalf("Write(half #2): %v", err)
	}
	if out.Len() != 2*(maxLine/2+1) {
		t.Errorf("sink = %d, want %d (whole accumulated buffer flushed)", out.Len(), 2*(maxLine/2+1))
	}
}

func TestFilteringWriterPropagatesSinkErrorOnCompleteLine(t *testing.T) {
	t.Parallel()

	sink := &errOnWrite{}
	fw := ioutil.NewFilteringWriter(sink)

	input := []byte("keep this line\n")
	n, err := fw.Write(input)
	if err == nil {
		t.Fatal("Write of a complete non-filtered line into a failing sink = nil error, want the sink error")
	}
	if n != len(input) {
		t.Errorf("Write = %d, want %d (full input length) even on sink error", n, len(input))
	}
	if sink.calls != 1 {
		t.Errorf("sink.Write called %d times, want 1 (the single complete line)", sink.calls)
	}
}

// TestProperty_FilteringWriterChunkInvariant asserts that FilteringWriter's
// filtered output is independent of how the input byte stream is split across
// Write calls, for inputs whose lines stay under maxLineBytes (the common
// fclones-output case). Lines are capped at 40 bytes so the no-newline
// cap-flush path never fires, under which chunk-invariance would not hold.
func TestProperty_FilteringWriterChunkInvariant(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		numLines := rapid.IntRange(0, 20).Draw(rt, "numLines")
		var stream []byte
		for range numLines {
			line := rapid.StringMatching(`[a-zA-Z0-9: ]{0,40}`).Draw(rt, "line")
			stream = append(stream, line...)
			stream = append(stream, '\n')
		}
		if rapid.Bool().Draw(rt, "trailingPartial") {
			tail := rapid.StringMatching(`[a-zA-Z0-9: ]{0,40}`).Draw(rt, "tail")
			stream = append(stream, tail...)
		}

		var refOut bytes.Buffer
		ref := ioutil.NewFilteringWriter(&refOut)
		if _, err := ref.Write(stream); err != nil {
			rt.Fatalf("reference Write: %v", err)
		}
		if err := ref.Flush(); err != nil {
			rt.Fatalf("reference Flush: %v", err)
		}

		var gotOut bytes.Buffer
		fw := ioutil.NewFilteringWriter(&gotOut)
		rest := stream
		for len(rest) > 0 {
			cut := rapid.IntRange(1, len(rest)).Draw(rt, "cut")
			if _, err := fw.Write(rest[:cut]); err != nil {
				rt.Fatalf("chunked Write: %v", err)
			}
			rest = rest[cut:]
		}
		if err := fw.Flush(); err != nil {
			rt.Fatalf("chunked Flush: %v", err)
		}

		if !bytes.Equal(refOut.Bytes(), gotOut.Bytes()) {
			rt.Fatalf("chunk-dependent output:\n whole   = %q\n chunked = %q", refOut.Bytes(), gotOut.Bytes())
		}
	})
}

func TestFilteringWriterCapResetsBufferAfterSinkError(t *testing.T) {
	t.Parallel()

	const cap = 1 << 20 // mirrors maxLineBytes

	sink := &failingThenRecordingSink{}
	fw := ioutil.NewFilteringWriter(sink)

	flood := bytes.Repeat([]byte("x"), cap+1)
	if _, err := fw.Write(flood); err == nil {
		t.Fatal("Write(flood) whose cap-flush hits a first-call-failing sink = nil error, want the sink error")
	}

	// The cap-flush resets the partial-line buffer even when the sink write
	// fails, so a following newline-terminated line must be emitted alone --
	// the >1MB flood must not be re-prefixed onto it.
	if _, err := fw.Write([]byte("next\n")); err != nil {
		t.Fatalf("Write(next) after the cap-flush sink error: %v", err)
	}
	if got := sink.got.String(); got != "next\n" {
		t.Errorf("after cap-flush sink error, sink recorded %d bytes, want exactly %q (buffer reset; flood not re-sent)", len(got), "next\n")
	}
}

type failingThenRecordingSink struct {
	got   bytes.Buffer
	calls int
}

func (s *failingThenRecordingSink) Write(p []byte) (int, error) {
	s.calls++
	if s.calls == 1 {
		return 0, os.ErrClosed
	}
	return s.got.Write(p)
}

func TestFilteringWriterClosePropagatesSinkError(t *testing.T) {
	t.Parallel()

	sink := &errOnWrite{}
	fw := ioutil.NewFilteringWriter(sink)

	if _, err := fw.Write([]byte("buffered partial line, no newline")); err != nil {
		t.Fatalf("Write(partial) buffered the line but returned error %v, want nil", err)
	}

	err := fw.Close()
	if err == nil {
		t.Fatal("Close of a buffered non-filtered line into a failing sink = nil error, want the sink error propagated from Flush")
	}
	if sink.calls != 1 {
		t.Errorf("sink.Write called %d times during Close, want 1", sink.calls)
	}
}

func TestFilteringWriterCapNotTrippedAtExactBoundary(t *testing.T) {
	t.Parallel()

	const maxLine = 1 << 20 // mirrors maxLineBytes

	var out bytes.Buffer
	fw := ioutil.NewFilteringWriter(&out)

	// A no-newline run of exactly maxLineBytes must NOT trip the cap: the guard
	// is `len(fw.buf) > maxLineBytes`, so the buffer is held (not flushed) until
	// it strictly exceeds the cap.
	if _, err := fw.Write(bytes.Repeat([]byte("x"), maxLine)); err != nil {
		t.Fatalf("Write(exact-cap): %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("sink got %d bytes after an exactly-maxLineBytes write, want 0 (cap not yet exceeded)", out.Len())
	}

	// One more no-newline byte pushes the buffer strictly past the cap, flushing.
	if _, err := fw.Write([]byte("y")); err != nil {
		t.Fatalf("Write(+1): %v", err)
	}
	if out.Len() != maxLine+1 {
		t.Errorf("sink got %d bytes after crossing the cap, want %d", out.Len(), maxLine+1)
	}
}

func TestProperty_ReadFileWithLimitMatchesFileSize(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		content := rapid.SliceOfN(rapid.Byte(), 0, 4096).Draw(rt, "content")
		limit := int64(rapid.IntRange(0, 8192).Draw(rt, "limit"))

		path := filepath.Join(t.TempDir(), "data.bin")
		if err := os.WriteFile(path, content, 0o644); err != nil {
			rt.Fatalf("WriteFile: %v", err)
		}

		data, err := ioutil.ReadFileWithLimit(path, limit)

		if int64(len(content)) <= limit {
			if err != nil {
				rt.Fatalf("ReadFileWithLimit(%d-byte file, limit=%d) = err %v, want nil", len(content), limit, err)
			}
			if !bytes.Equal(data, content) {
				rt.Fatalf("ReadFileWithLimit returned %d bytes, want the %d-byte file content verbatim", len(data), len(content))
			}
		} else {
			if err == nil {
				rt.Fatalf("ReadFileWithLimit(%d-byte file, limit=%d) = nil error, want an over-limit error", len(content), limit)
			}
			if data != nil {
				rt.Fatalf("ReadFileWithLimit over-limit returned %d bytes, want nil data alongside the error", len(data))
			}
		}
	})
}
