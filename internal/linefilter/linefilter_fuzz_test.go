package linefilter_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/cplieger/docker-fclones-scheduler/internal/linefilter"
)

func FuzzWriter(f *testing.F) {
	f.Add("info: Started grouping\nkeep me\n")
	f.Add("no newline at all")
	f.Add(strings.Repeat("a", linefilter.MaxLineBytes+1)) // no-newline flood-flush path
	f.Add("")
	f.Add("\n\n\n")
	f.Add("info: Scanned 5\npartial line")
	f.Add("cannot read file /scan/\x1b[31mevil\x1b[0m: denied\n")
	f.Add("cannot read file /scan/a\x00\x7fb: denied\n")
	f.Add("\x1b")
	f.Fuzz(func(t *testing.T, input string) {
		var sink bytes.Buffer
		fw := linefilter.New(&sink)

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

		// Bounded expansion: each C0/DEL/invalid-UTF-8 byte rewrites to a 4-byte
		// \xNN escape, so output can exceed input but never by more than 4x.
		if len(out) > 4*len(input) {
			t.Fatalf("filtered output %d bytes exceeds 4x input %d bytes for %q", len(out), len(input), input)
		}

		// CWE-117: no forbidden control byte (C0 except '\n'/'\t', or DEL) may
		// survive into the log sink.
		for _, b := range out {
			if (b < 0x20 && b != '\n' && b != '\t') || b == 0x7f {
				t.Fatalf("forbidden control byte %#02x survived into output %q for input %q", b, out, input)
			}
		}

		// Independent oracle (not shouldFilterLine's own code): a noise marker is
		// dropped only inside the message body of a genuine "fclones: <level>:"
		// line whose level matches the marker. The FIEMAP marker also requires a
		// prefix anchor, so a same-level warn merely echoing the phrase survives.
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
				continue // partial tail, not a complete line
			}
			if filtered(line) {
				t.Fatalf("emitted complete line %q would be filtered for input %q", line, input)
			}
		}
	})
}
