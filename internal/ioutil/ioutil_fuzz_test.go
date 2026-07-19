package ioutil_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/cplieger/docker-fclones-scheduler/internal/ioutil"
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
	f.Add(strings.Repeat("a", ioutil.MaxLineBytes+1)) // > MaxLineBytes: covers the no-newline flood-flush path
	f.Add("")
	f.Add("\n\n\n")
	f.Add("info: Scanned 5\npartial line")
	f.Add("cannot read file /scan/\x1b[31mevil\x1b[0m: denied\n") // ESC escapes to \xNN; output exceeds input
	f.Add("cannot read file /scan/a\x00\x7fb: denied\n")          // NUL + DEL escape
	f.Add("\x1b")                                                 // lone control byte, no newline: sanitized on Flush
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

		// Bounded expansion: FilteringWriter drops whole lines and rewrites each C0/DEL
		// control byte (and any invalid-UTF-8 byte) to a 4-byte \xNN escape via
		// sanitizeControlBytes, so output can exceed input, but never by more than 4x.
		// The old drop-only bound (len(out) > len(input)) held only before the
		// sanitizer landed; a control byte in a scanned path trips it under the
		// coverage-guided fuzz run even though the deterministic seed corpus passed.
		if len(out) > 4*len(input) {
			t.Fatalf("filtered output %d bytes exceeds 4x input %d bytes for %q", len(out), len(input), input)
		}

		// Sanitization efficacy (CWE-117): no forbidden control byte (C0 except
		// '\n'/'\t', or DEL) may survive into the log sink, however it was framed in
		// the raw scanned-path text fclones renders.
		for _, b := range out {
			if (b < 0x20 && b != '\n' && b != '\t') || b == 0x7f {
				t.Fatalf("forbidden control byte %#02x survived into output %q for input %q", b, out, input)
			}
		}

		// Filter efficacy: every COMPLETE (newline-terminated) line emitted must be a
		// non-filtered line. An independent oracle (NOT shouldFilterLine's own code)
		// mirrors the positional policy so a wrong edit to either side is caught: a
		// noise marker is dropped only inside the message body of a genuine
		// "fclones: <level>:" line whose level matches the marker (info-progress
		// markers at info, the FIEMAP notice at warn). The FIEMAP marker also carries
		// a prefix anchor ("File system "): the body must BEGIN with fclones' own
		// framing for the canonical notice, so a genuine same-level warn about an
		// attacker-named path that merely echoes the phrase survives. A
		// filter-disabled mutant of emit() makes a filtered line survive here even
		// though the byte-bound passes.
		type marker struct{ substr, prefix string }
		noiseByLevel := map[string][]marker{
			"info": {{substr: "Started grouping"}, {substr: "Started deduplicating"}, {substr: "Scanned "}, {substr: "Found "}},
			"warn": {{substr: "doesn't support FIEMAP ioctl API", prefix: "File system "}},
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
			body := strings.TrimLeft(msg, " ")
			for _, m := range noiseByLevel[strings.TrimSpace(level)] {
				if m.prefix != "" && !strings.HasPrefix(body, m.prefix) {
					continue
				}
				if strings.Contains(body, m.substr) {
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
