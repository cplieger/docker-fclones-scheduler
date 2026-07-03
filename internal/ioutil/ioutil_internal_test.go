package ioutil

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// shouldFilterLine is unexported (no production caller outside this package),
// so its tests live here in the internal (package ioutil) test file rather
// than the external ioutil_test package. The external tests cover the public
// surface (FilteringWriter / LimitedBuffer / ReadFileWithLimit); these pin the
// noise-filter predicate directly. FuzzShouldFilterLine deliberately
// re-declares the noise pattern lists as an independent level-keyed oracle (not
// the production infoProgressPatterns/warnNoisePatterns vars), so a wrong edit to
// either side is caught.

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
		{"[2026-04-26 11:00:03.072] fclones: warn: cannot read file /scandir/Scanned 5 backups.txt", false},
		{"[2026-04-26 11:00:03.072] fclones: error: failed on /scandir/Found duplicates/x", false},
		// FIEMAP phrase echoed in an attacker-controlled scanned filename inside a
		// genuine error line must NOT be suppressed (h-f1: the FIEMAP marker is a
		// warn-level body marker, so a real error diagnostic that merely names such
		// a file at a different level still reaches the logs).
		{"[2026-04-26 11:00:03.072] fclones: error: cannot read /scandir/doesn't support FIEMAP ioctl API.txt", false},
		// An info progress marker ("Found ") echoed in an error filename body must
		// likewise survive: progress markers are info-level, so they cannot suppress
		// a genuine error line that happens to name such a file.
		{"[2026-04-26 11:00:03.072] fclones: error: cannot read /scandir/Found duplicates.txt", false},
		// SAME-LEVEL guard (l-f3): a genuine warn ABOUT an attacker-named scanned
		// path whose body merely echoes the FIEMAP phrase must NOT be suppressed.
		// fclones frames the canonical notice as "File system <fs> ... doesn't
		// support FIEMAP ioctl API" and frames path read-errors as "cannot read
		// file ...", so the prefix anchor ("File system ") distinguishes them: this
		// body begins with "cannot read file", not "File system ", so it survives.
		{"[2026-04-26 11:00:03.072] fclones: warn: cannot read file /scandir/x doesn't support FIEMAP ioctl API: permission denied", false},
		{"Scanned 100 file entries", false},
		{"[2026-04-26 11:00:03.072] fclones: error: Started grouping cache rebuild failed", false},
		{`time=... level=INFO msg="scan complete" scan_id=abc`, false},
		{"", false},
		// A warn-only marker (the FIEMAP notice) appearing as the GENUINE body of
		// an info: line must NOT be suppressed: the marker is registered only at
		// warn level, so level-keyed matching leaves it alone at info level. Pins
		// the e210e9d guarantee against a mutant that drops the level key and
		// matches every marker set regardless of level.
		{"[2026-04-26 11:00:03.072] fclones:  info: doesn't support FIEMAP ioctl API today", false},
		// Symmetrically, an info progress marker ("Started grouping") as the
		// genuine body of a warn: line must NOT be suppressed: progress markers are
		// registered only at info level.
		{"[2026-04-26 11:00:03.072] fclones:  warn: Started grouping the cache", false},
		// fclones: prefix present but the remainder has no second colon (no level
		// separator). shouldFilterLine returns false at the second strings.Cut
		// guard; this pins ioutil.go's malformed-level branch, which the well-formed
		// table cases and the (prefix-free) fuzz seeds never reach deterministically.
		{"[2026-04-26 11:00:03.072] fclones: warnnocolon doesn't support FIEMAP ioctl API", false},
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			t.Parallel()
			if got := shouldFilterLine(tt.line); got != tt.drop {
				t.Errorf("shouldFilterLine(%q) = %v, want %v", tt.line, got, tt.drop)
			}
		})
	}
}

func FuzzShouldFilterLine(f *testing.F) {
	f.Add("info: Started grouping files")
	f.Add("info: Scanned 100 files")
	f.Add("doesn't support FIEMAP ioctl API")
	f.Add("normal output line")
	f.Add("")
	// Genuine drops at each registered level (exercise the drop path itself).
	// The warn seed uses fclones' canonical "File system " framing so it satisfies
	// the FIEMAP marker's prefix anchor and actually drops.
	f.Add("[ts] fclones:  info: Started grouping")
	f.Add("[ts] fclones: warn: File system zfs on device data/media doesn't support FIEMAP ioctl API. harmless.")
	// Same-level guard: a genuine warn ABOUT an attacker-named path that merely
	// echoes the FIEMAP phrase ("cannot read file ..." framing, not "File system ")
	// must NOT drop (oracle computes want=false against the prefix anchor).
	f.Add("[ts] fclones: warn: cannot read file /scandir/x doesn't support FIEMAP ioctl API: denied")
	// Cross-level false-suppression guarantee: a marker echoed in a filename at a
	// different level, and a marker as the genuine body at the wrong level, must
	// both survive (oracle computes want=false for these).
	f.Add("[ts] fclones: error: cannot read /scandir/doesn't support FIEMAP ioctl API.txt")
	f.Add("[ts] fclones:  info: doesn't support FIEMAP ioctl API today")
	// fclones: prefix but no second colon (malformed level): not filtered.
	f.Add("[ts] fclones: warnnocolon doesn't support FIEMAP ioctl API")
	f.Fuzz(func(t *testing.T, input string) {
		// Independent oracle mirroring shouldFilterLine's positional policy:
		// a noise marker is dropped only when it appears in the message body of a
		// genuine "fclones: <level>:" line whose level matches the marker's level
		// (info-progress markers at info, the FIEMAP notice at warn). The FIEMAP
		// marker also carries a prefix anchor ("File system "): the body must BEGIN
		// with fclones' own framing for the canonical notice, so a marker echoed in
		// a scanned filename inside a genuine same-level warn about an attacker-named
		// path is NOT filtered. A marker planted anywhere else (echoed at a different
		// level, or ahead of the fclones prefix) must NOT be filtered.
		type marker struct{ substr, prefix string }
		noiseByLevel := map[string][]marker{
			"info": {{substr: "Started grouping"}, {substr: "Started deduplicating"}, {substr: "Scanned "}, {substr: "Found "}},
			"warn": {{substr: "doesn't support FIEMAP ioctl API", prefix: "File system "}},
		}
		want := false
		const prefix = "fclones:"
		if _, after, found := strings.Cut(input, prefix); found {
			rest := strings.TrimLeft(after, " ")
			if level, msg, ok := strings.Cut(rest, ":"); ok {
				body := strings.TrimLeft(msg, " ")
				for _, m := range noiseByLevel[strings.TrimSpace(level)] {
					if m.prefix != "" && !strings.HasPrefix(body, m.prefix) {
						continue
					}
					if strings.Contains(body, m.substr) {
						want = true
						break
					}
				}
			}
		}
		if got := shouldFilterLine(input); got != want {
			t.Fatalf("shouldFilterLine(%q) = %v, want %v", input, got, want)
		}
	})
}

// FuzzSanitizeControlBytes pins the properties of the unexported sanitizer
// directly (FilteringWriter as a whole is not idempotent because of line
// framing, so idempotency can only be asserted on the function itself). The
// seed corpus runs deterministically every PR; the properties are asserted
// against the post-UTF-8-awareness behavior (a standalone C1 / invalid byte is
// escaped, valid multi-byte runes pass through verbatim).
func FuzzSanitizeControlBytes(f *testing.F) {
	f.Add("clean printable text /scan/file.txt")
	f.Add("esc \x1b[31mred\x1b[0m reset")
	f.Add("nul\x00 and del\x7f bytes")
	f.Add("tab\there newline\nkept")
	f.Add("valid multibyte caf\u00e9 \u65e5\u672c\u8a9e kept")
	f.Add("standalone C1 \x9b and invalid \xff escaped")
	f.Add("")
	f.Fuzz(func(t *testing.T, input string) {
		out := sanitizeControlBytes([]byte(input))

		// Efficacy (CWE-117 log injection): no forbidden control byte -- any C0 byte
		// other than '\n'/'\t', or DEL -- survives into the sanitized output.
		for _, b := range out {
			if (b < 0x20 && b != '\n' && b != '\t') || b == 0x7f {
				t.Fatalf("forbidden control byte %#02x survived in %q for input %q", b, out, input)
			}
		}

		// Idempotency (required of any sanitizer): the escaped form is valid UTF-8
		// containing only printable ASCII escapes plus preserved '\n'/'\t' and valid
		// runes, so a second pass takes the fast path and changes nothing.
		if again := sanitizeControlBytes(out); string(again) != string(out) {
			t.Fatalf("not idempotent: sanitize(%q) = %q, second pass = %q", input, out, again)
		}

		// Bounded transform: escaping is the only growth (1 byte -> 4 bytes) and
		// nothing is dropped, so output never shrinks and never exceeds 4x the input.
		if len(out) < len(input) || len(out) > 4*len(input) {
			t.Fatalf("output length %d outside [%d, %d] for input %q", len(out), len(input), 4*len(input), input)
		}

		// Identity fast path: input already valid UTF-8 with nothing to escape is
		// returned verbatim. The verbatim condition is UTF-8-aware, so a standalone
		// C1 / invalid byte (>=0x80 but not part of a valid rune) is NOT clean even
		// though it is neither a C0 control nor DEL -- it is escaped instead.
		clean := utf8.Valid([]byte(input))
		if clean {
			for _, b := range []byte(input) {
				if (b < 0x20 && b != '\n' && b != '\t') || b == 0x7f {
					clean = false
					break
				}
			}
		}
		if clean && string(out) != input {
			t.Fatalf("clean input mutated: sanitize(%q) = %q", input, out)
		}
	})
}
