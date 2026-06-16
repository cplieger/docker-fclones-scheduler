package ioutil_test

import (
	"bytes"
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

// --- Tests: shouldFilterLine ---

func TestShouldFilterLine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		line string
		drop bool
	}{
		{"[2026-04-26 11:00:03.020] fclones: warn: File system zfs on device data/media doesn't support FIEMAP ioctl API. This is generally harmless.", true},
		{"[2026-04-26 11:00:00.098] fclones:  info: Started grouping", true},
		{"[2026-04-26 11:00:00.098] fclones:  info: Started deduplicating", true},
		{"[2026-04-26 11:00:02.868] fclones:  info: Scanned 238597 file entries", true},
		{"[2026-04-26 11:00:03.019] fclones:  info: Found 1148 (291.3 GB) candidates after grouping by size", true},
		{"[2026-04-26 11:00:03.072] fclones:  info: Processed 6 files and reclaimed 15.1 KB space", false},
		{"[2026-04-26 11:00:03.072] fclones:  warn: cannot read file /scandir/broken: permission denied", false},
		{"[2026-04-26 11:00:03.072] fclones: error: cache corruption detected", false},
		{`time=... level=INFO msg="scan complete" scan_id=abc`, false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			t.Parallel()
			if got := ioutil.ShouldFilterLine(tt.line); got != tt.drop {
				t.Errorf("ShouldFilterLine(%q) = %v, want %v", tt.line, got, tt.drop)
			}
		})
	}
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
