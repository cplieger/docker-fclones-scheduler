package linefilter_test

import (
	"bytes"
	"os"
	"testing"

	"github.com/cplieger/docker-fclones-scheduler/internal/linefilter"
	"pgregory.net/rapid"
)

func TestWriterDropsFilteredLines(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	fw := linefilter.New(&out)

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

func TestWriterHandlesPartialLines(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	fw := linefilter.New(&out)

	fw.Write([]byte("[ts] fclones:  info: Start"))
	fw.Write([]byte("ed grouping\nkeep this\n"))

	got := out.String()
	if got != "keep this\n" {
		t.Errorf("got %q, want %q", got, "keep this\n")
	}
}

func TestWriterSanitizesControlBytes(t *testing.T) {
	t.Parallel()
	// fclones renders scanned filenames raw into stderr, and a filename is
	// attacker-influenceable. emit rewrites C0 (except '\n'/'\t'), DEL, the
	// C1 control block (even 2-byte-UTF-8-encoded), and any invalid UTF-8
	// byte to a visible \xNN escape (CWE-117), leaving other runes intact.
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "ANSI escape in a scanned filename is neutralized",
			input: "[ts] fclones:  warn: cannot read file /scan/\x1b[31mevil\x1b[0m: denied\n",
			want:  "[ts] fclones:  warn: cannot read file /scan/\\x1b[31mevil\\x1b[0m: denied\n",
		},
		{
			name:  "carriage return and NUL are escaped",
			input: "cannot read file /scan/keep\x0d\x00name: denied\n",
			want:  "cannot read file /scan/keep\\x0d\\x00name: denied\n",
		},
		{
			name:  "DEL is escaped",
			input: "cannot read file /scan/a\x7fb: denied\n",
			want:  "cannot read file /scan/a\\x7fb: denied\n",
		},
		{
			name:  "tab and multibyte UTF-8 filename are preserved verbatim",
			input: "cannot read file /scan/caf\u00e9\t\u65e5\u672c\u8a9e: denied\n",
			want:  "cannot read file /scan/caf\u00e9\t\u65e5\u672c\u8a9e: denied\n",
		},
		{
			// 0x9B is the 8-bit C1 CSI, invalid standing alone as UTF-8.
			name:  "standalone C1 CSI byte is escaped while valid UTF-8 is preserved",
			input: "cannot read file /scan/caf\u00e9\x9b: denied\n",
			want:  "cannot read file /scan/caf\u00e9\\x9b: denied\n",
		},
		{
			// 0x80 is the lowest UTF-8 continuation byte and the exact edge of
			// the ASCII test (utf8.RuneSelf); a wrong comparison leaks this
			// byte specifically.
			name:  "standalone 0x80 continuation byte is escaped",
			input: "cannot read file /scan/a\x80b: denied\n",
			want:  "cannot read file /scan/a\\x80b: denied\n",
		},
		{
			// The well-formed 2-byte C1 CSI (U+009B) must still be escaped
			// despite being valid UTF-8; a byte-only escaper would miss it.
			name:  "well-formed multibyte C1 CSI is escaped, NBSP and accents preserved",
			input: "cannot read file /scan/caf\u00e9\u009b\u00a0x: denied\n",
			want:  "cannot read file /scan/caf\u00e9\\xc2\\x9b\u00a0x: denied\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			fw := linefilter.New(&out)
			if _, err := fw.Write([]byte(tc.input)); err != nil {
				t.Fatalf("Write: %v", err)
			}
			if got := out.String(); got != tc.want {
				t.Errorf("sanitize mismatch\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}

// TestWriterPreservesMultibyteRuneSplitAcrossWrites guards a multi-byte rune
// split across separate Write calls (as a subprocess pipe read can split
// mid-rune): it must be reassembled into the complete line before
// sanitization runs, so it is forwarded verbatim rather than escaped as two
// invalid bytes.
func TestWriterPreservesMultibyteRuneSplitAcrossWrites(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	fw := linefilter.New(&out)

	// "café" ends in U+00E9 (0xC3 0xA9); deliver the lead and continuation
	// bytes in separate Write calls.
	line := []byte("cannot read file /scan/caf\u00e9: denied\n")
	idx := bytes.IndexByte(line, 0xC3)
	if idx < 0 {
		t.Fatal("setup: expected a 0xC3 UTF-8 lead byte in the test line")
	}

	if _, err := fw.Write(line[:idx+1]); err != nil {
		t.Fatalf("Write(head ending on the rune lead byte): %v", err)
	}
	if _, err := fw.Write(line[idx+1:]); err != nil {
		t.Fatalf("Write(tail starting on the rune continuation byte): %v", err)
	}
	if err := fw.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	if got := out.String(); got != string(line) {
		t.Errorf("split multibyte rune corrupted\n got: %q\nwant: %q", got, string(line))
	}
}

// TestWriterWritesLeadingBlankLine guards the newline-search boundary:
// a buffer whose first byte is '\n' must still be forwarded, not buffered
// (mutating `idx < 0` to `idx <= 0` would swallow the whole write).
func TestWriterWritesLeadingBlankLine(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	fw := linefilter.New(&out)

	if _, err := fw.Write([]byte("\nkeep this\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got := out.String()
	want := "\nkeep this\n"
	if got != want {
		t.Errorf("Write(%q) wrote %q, want %q", "\nkeep this\n", got, want)
	}
}

func TestWriterFlushEmitsBufferedPartialLine(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	fw := linefilter.New(&out)
	fw.Write([]byte("partial line without newline"))
	fw.Flush()
	if got := out.String(); got != "partial line without newline" {
		t.Errorf("after Flush, out = %q, want partial line", got)
	}
}

func TestWriterFlushDropsFilteredLine(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	fw := linefilter.New(&out)
	fw.Write([]byte("[ts] fclones:  info: Started grouping"))
	fw.Flush()
	if got := out.String(); got != "" {
		t.Errorf("after Flush of filtered line, out = %q, want empty", got)
	}
}

func TestWriterFlushEmptyIsNoop(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	fw := linefilter.New(&out)
	if err := fw.Flush(); err != nil {
		t.Fatalf("Flush on empty buffer: %v", err)
	}
}

func TestWriterFlushPropagatesSinkError(t *testing.T) {
	t.Parallel()

	sink := &errOnWrite{}
	fw := linefilter.New(sink)

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

func TestWriterCapsUnboundedNoNewlineFlood(t *testing.T) {
	t.Parallel()

	const cap = linefilter.MaxLineBytes

	t.Run("flushes oversized non-filtered partial line and resets buffer", func(t *testing.T) {
		t.Parallel()
		var out bytes.Buffer
		fw := linefilter.New(&out)

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

		// Buffer resets at the cap: a following line is emitted alone, not
		// concatenated with the flushed flood.
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
		fw := linefilter.New(&out)

		// A >1MB no-newline run carrying a genuine "fclones: info:" noise
		// marker: the cap fires and the filtered line is dropped.
		flood := append([]byte("[ts] fclones:  info: Scanned "), bytes.Repeat([]byte("9"), cap+1)...)
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
		fw := linefilter.New(sink)

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

func TestWriterCapFiresAcrossMultipleWrites(t *testing.T) {
	t.Parallel()
	const maxLine = linefilter.MaxLineBytes
	var out bytes.Buffer
	fw := linefilter.New(&out)
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

func TestFloodsCountsForcedFlushes(t *testing.T) {
	t.Parallel()
	const maxLine = linefilter.MaxLineBytes
	var out bytes.Buffer
	fw := linefilter.New(&out)

	if got := fw.Floods(); got != 0 {
		t.Errorf("Floods() = %d before any write, want 0", got)
	}

	// Two separate no-newline runs each exceed MaxLineBytes, so each forces
	// a cap-flush and bumps the flood counter.
	if _, err := fw.Write(bytes.Repeat([]byte("x"), maxLine+1)); err != nil {
		t.Fatalf("Write(flood #1): %v", err)
	}
	if got := fw.Floods(); got != 1 {
		t.Errorf("Floods() = %d after one no-newline flood, want 1", got)
	}

	if _, err := fw.Write(bytes.Repeat([]byte("y"), maxLine+1)); err != nil {
		t.Fatalf("Write(flood #2): %v", err)
	}
	if got := fw.Floods(); got != 2 {
		t.Errorf("Floods() = %d after a second flood, want 2", got)
	}

	// A sub-cap, newline-terminated line must not bump the flood counter.
	if _, err := fw.Write([]byte("short line\n")); err != nil {
		t.Fatalf("Write(short): %v", err)
	}
	if got := fw.Floods(); got != 2 {
		t.Errorf("Floods() = %d after a sub-cap line, want 2 (no new flood)", got)
	}
}

func TestWriterPropagatesSinkErrorOnCompleteLine(t *testing.T) {
	t.Parallel()

	sink := &errOnWrite{}
	fw := linefilter.New(sink)

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

// TestProperty_WriterChunkInvariant asserts filtered output is independent
// of how the input is split across Write calls, for lines under
// MaxLineBytes (capped at 40 bytes so the no-newline cap-flush path never
// fires, under which chunk-invariance would not hold).
func TestProperty_WriterChunkInvariant(t *testing.T) {
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
		ref := linefilter.New(&refOut)
		if _, err := ref.Write(stream); err != nil {
			rt.Fatalf("reference Write: %v", err)
		}
		if err := ref.Flush(); err != nil {
			rt.Fatalf("reference Flush: %v", err)
		}

		var gotOut bytes.Buffer
		fw := linefilter.New(&gotOut)
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

func TestWriterCapResetsBufferAfterSinkError(t *testing.T) {
	t.Parallel()

	const cap = linefilter.MaxLineBytes

	sink := &failingThenRecordingSink{}
	fw := linefilter.New(sink)

	flood := bytes.Repeat([]byte("x"), cap+1)
	if _, err := fw.Write(flood); err == nil {
		t.Fatal("Write(flood) whose cap-flush hits a first-call-failing sink = nil error, want the sink error")
	}

	// The cap-flush resets the buffer even when the sink write fails, so the
	// >1MB flood must not be re-prefixed onto the following line.
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

func TestWriterCapNotTrippedAtExactBoundary(t *testing.T) {
	t.Parallel()

	const maxLine = linefilter.MaxLineBytes

	var out bytes.Buffer
	fw := linefilter.New(&out)

	// A no-newline run of exactly MaxLineBytes must not trip the cap: the
	// guard is `len(fw.buf) > MaxLineBytes`, held until strictly exceeded.
	if _, err := fw.Write(bytes.Repeat([]byte("x"), maxLine)); err != nil {
		t.Fatalf("Write(exact-cap): %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("sink got %d bytes after an exactly-MaxLineBytes write, want 0 (cap not yet exceeded)", out.Len())
	}

	// One more byte pushes strictly past the cap, flushing.
	if _, err := fw.Write([]byte("y")); err != nil {
		t.Fatalf("Write(+1): %v", err)
	}
	if out.Len() != maxLine+1 {
		t.Errorf("sink got %d bytes after crossing the cap, want %d", out.Len(), maxLine+1)
	}
}

// TestWriterFloodThresholdTracksMaxLineBytes pins the flood threshold to the
// exported MaxLineBytes constant scheduler.go logs as cap_bytes, so the
// operator-facing cap can never drift from the real flush boundary.
func TestWriterFloodThresholdTracksMaxLineBytes(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	fw := linefilter.New(&out)

	if _, err := fw.Write(bytes.Repeat([]byte("x"), linefilter.MaxLineBytes)); err != nil {
		t.Fatalf("Write(exact MaxLineBytes): %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("sink got %d bytes at exactly MaxLineBytes, want 0 (held, cap not exceeded)", out.Len())
	}
	if got := fw.Floods(); got != 0 {
		t.Errorf("Floods() = %d at exactly MaxLineBytes, want 0", got)
	}

	if _, err := fw.Write([]byte("y")); err != nil {
		t.Fatalf("Write(+1 past MaxLineBytes): %v", err)
	}
	if out.Len() != linefilter.MaxLineBytes+1 {
		t.Errorf("sink got %d bytes after crossing MaxLineBytes, want %d", out.Len(), linefilter.MaxLineBytes+1)
	}
	if got := fw.Floods(); got != 1 {
		t.Errorf("Floods() = %d after crossing MaxLineBytes, want 1", got)
	}
}

// TestEscapeUnsafeMatchesPassthroughPolicy pins the exported escaper
// (streamAttrs) to the same policy the stderr passthrough applies, so a
// drift between the two sinks can't reopen CWE-117 on whichever lagged.
func TestEscapeUnsafeMatchesPassthroughPolicy(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, in, want string }{
		{"ESC escaped", "a\x1b[31mred", `a\x1b[31mred`},
		{"RLO bidi override escaped", "evil\u202egnp.txt", `evil\xe2\x80\xaegnp.txt`},
		{"U+2028 escaped", "x\u2028y", `x\xe2\x80\xa8y`},
		{"multi-line tail keeps framing", "line1\nline2\tcol", "line1\nline2\tcol"},
		{"legit unicode verbatim", "caf\u00e9 \u65e5\u672c 50\u2030", "caf\u00e9 \u65e5\u672c 50\u2030"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := linefilter.EscapeUnsafe(tc.in); got != tc.want {
				t.Errorf("EscapeUnsafe(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
