package ioutil_test

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/fclones-wrapper/internal/ioutil"
)

func FuzzLimitedBuffer(f *testing.F) {
	f.Add([]byte("hello world"), 5)
	f.Add([]byte("short"), 100)
	f.Add([]byte(""), 0)
	f.Add([]byte("exactly"), 7)
	f.Add([]byte("negative cap"), -5)
	f.Fuzz(func(t *testing.T, data []byte, max int) {
		lb := &ioutil.LimitedBuffer{Max: max}
		n, err := lb.Write(data)
		if err != nil {
			t.Fatalf("Write should never error, got: %v", err)
		}
		if n != len(data) {
			t.Fatalf("Write returned %d, want %d", n, len(data))
		}
		// A non-positive Max clamps to an effective cap of 0 (the
		// max(0, lb.Max-lb.buf.Len()) guard in Write), so the buffer content
		// must never exceed max(0, max).
		effCap := max
		if effCap < 0 {
			effCap = 0
		}
		if len(lb.String()) > effCap {
			t.Fatalf("buffer len %d exceeds effective cap %d", len(lb.String()), effCap)
		}
		// Total must equal bytes written
		if lb.Total() != len(data) {
			t.Fatalf("Total() = %d, want %d", lb.Total(), len(data))
		}
		// Truncated iff wrote more than max
		if lb.Truncated() != (len(data) > max) {
			t.Fatalf("Truncated() = %v, want %v", lb.Truncated(), len(data) > max)
		}
	})
}

func FuzzFilteringWriter(f *testing.F) {
	f.Add("info: Started grouping\nkeep me\n")
	f.Add("no newline at all")
	f.Add(strings.Repeat("a", 1<<20+1)) // >maxLineBytes: covers the no-newline flood-flush path
	f.Add("")
	f.Add("\n\n\n")
	f.Add("info: Scanned 5\npartial line")
	f.Fuzz(func(t *testing.T, input string) {
		var sink bytes.Buffer
		fw := ioutil.NewFilteringWriter(&sink)

		n, err := fw.Write([]byte(input))
		if err != nil {
			t.Fatalf("Write(%q) into an in-memory sink returned error %v, want nil", input, err)
		}
		if n != len(input) {
			t.Fatalf("Write(%q) returned n=%d, want %d (full input length)", input, n, len(input))
		}
		if err := fw.Flush(); err != nil {
			t.Fatalf("Flush after Write(%q) returned error %v, want nil", input, err)
		}

		out := sink.Bytes()

		// Drop-only: FilteringWriter only ever drops whole lines, so it must never
		// emit more bytes than it received -- regardless of how lines split across
		// the internal buffer or the no-newline cap-flush path.
		if len(out) > len(input) {
			t.Fatalf("filtered output %d bytes exceeds input %d bytes for %q", len(out), len(input), input)
		}

		// Filter efficacy: every COMPLETE (newline-terminated) line emitted must be a
		// non-filtered line. An independent oracle (NOT shouldFilterLine's own code)
		// mirrors the positional policy so a wrong edit to either side is caught: a
		// noise marker is dropped only inside the message body of a genuine
		// "fclones: <level>:" line whose level matches the marker (info-progress
		// markers at info, the FIEMAP notice at warn). A filter-disabled mutant of
		// emit() makes a filtered line survive here even though the byte-bound passes.
		noiseByLevel := map[string][]string{
			"info": {"Started grouping", "Started deduplicating", "Scanned ", "Found "},
			"warn": {"doesn't support FIEMAP ioctl API"},
		}
		filtered := func(line string) bool {
			const prefix = "fclones:"
			_, after, ok := strings.Cut(line, prefix)
			if !ok {
				return false
			}
			rest := strings.TrimLeft(after, " ")
			level, msg, ok := strings.Cut(rest, ":")
			if !ok {
				return false
			}
			for _, p := range noiseByLevel[strings.TrimSpace(level)] {
				if strings.Contains(msg, p) {
					return true
				}
			}
			return false
		}
		for _, line := range strings.SplitAfter(string(out), "\n") {
			if !strings.HasSuffix(line, "\n") {
				continue // trailing fragment is the unflushed/partial tail, not a complete line
			}
			if filtered(line) {
				t.Fatalf("emitted complete line %q would be filtered for input %q", line, input)
			}
		}
	})
}

func FuzzReadFileWithLimit(f *testing.F) {
	f.Add([]byte("hello"), int64(100))
	f.Add([]byte(""), int64(0))
	f.Add([]byte("exact"), int64(5))
	f.Add([]byte("over the limit"), int64(1))
	f.Add([]byte(strings.Repeat("x", 70000)), int64(65536))
	f.Add([]byte("near-max"), int64(math.MaxInt64))
	f.Add([]byte("near-max-1"), int64(math.MaxInt64-1))
	f.Add([]byte("neg"), int64(-1))
	f.Fuzz(func(t *testing.T, content []byte, limit int64) {
		// Bound the on-disk fixture (not the limit): cap the file at 1 MiB so
		// the temp write stays cheap, while leaving `limit` fully fuzzable across
		// the int64 range so the coverage-guided run can reach the MaxInt64
		// overflow guard and the negative-limit rejection path.
		if len(content) > 1<<20 {
			content = content[:1<<20]
		}

		path := filepath.Join(t.TempDir(), "data.bin")
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		data, err := ioutil.ReadFileWithLimit(path, limit)
		if err != nil {
			if int64(len(content)) <= limit {
				t.Fatalf("ReadFileWithLimit(%d-byte file, limit=%d) errored %v, want success", len(content), limit, err)
			}
			if data != nil {
				t.Fatalf("error path returned %d bytes, want nil data", len(data))
			}
			return
		}
		if int64(len(data)) > limit {
			t.Fatalf("returned %d bytes exceeds cap %d", len(data), limit)
		}
		if string(data) != string(content) {
			t.Fatalf("returned %d bytes, want the %d-byte file content verbatim", len(data), len(content))
		}
	})
}
