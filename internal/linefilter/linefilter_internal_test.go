package linefilter

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/cplieger/runesafe/v2"
)

// shouldFilterLine is unexported (no production caller outside this package),
// so its tests live here in the internal (package linefilter) test file rather
// than the external linefilter_test package. The external tests cover the public
// surface (Writer / capbuf.Buffer); these pin the
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
		// guard; this pins linefilter.go's malformed-level branch, which the well-formed
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

// TestSanitizeControlBytesEscapesUnsafeRunes pins the well-formed-rune half of
// the escape class, which is runesafe.IsUnsafeNonASCII's: the C1 control block
// U+0080..U+009F even in its 2-byte UTF-8 encoding (terminal escape
// introducers), the Unicode bidi controls (Trojan-Source reordering), and
// U+2028/U+2029 (record forgery in Unicode-line-terminator-splitting
// consumers) -- each a CWE-117 vector on an attacker-influenced scanned
// filename. The boundaries are exact: the nearest sibling codepoint outside
// each class (NBSP U+00A0 past the C1 block, U+202F past the U+202A-202E bidi
// run, U+2065 in the gap before the isolates, U+2030 past U+2029) and ordinary
// non-ASCII (accented Latin, CJK) are forwarded verbatim. Calls
// sanitizeControlBytes directly (byte-in / byte-out) so both code paths are
// exercised: an input made only of an unsafe rune would otherwise take the
// no-alloc fast path.
func TestSanitizeControlBytesEscapesUnsafeRunes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   []byte
		want string
	}{
		{
			name: "U+009B CSI as valid 2-byte UTF-8 is escaped",
			in:   []byte("a\u009bb"), // 0x61 0xC2 0x9B 0x62
			want: `a\xc2\x9bb`,
		},
		{
			name: "U+0080 low edge of the C1 block is escaped",
			in:   []byte("\u0080"), // 0xC2 0x80
			want: `\xc2\x80`,
		},
		{
			name: "U+009F high edge of the C1 block is escaped",
			in:   []byte("\u009f"), // 0xC2 0x9F
			want: `\xc2\x9f`,
		},
		{
			name: "U+00A0 NBSP just past the C1 block is forwarded verbatim",
			in:   []byte("x\u00a0y"), // 0xC2 0xA0
			want: "x\u00a0y",
		},
		{
			name: "U+00E9 e-acute is forwarded verbatim",
			in:   []byte("caf\u00e9"), // 0xC3 0xA9
			want: "caf\u00e9",
		},
		{
			name: "CJK runes are forwarded verbatim",
			in:   []byte("\u65e5\u672c\u8a9e"),
			want: "\u65e5\u672c\u8a9e",
		},
		{
			// A valid rune (é) adjacent to a C1 control forces the slow path: the
			// é must survive verbatim while only the C1 rune's bytes are escaped.
			name: "C1 escaped while an adjacent valid rune survives",
			in:   []byte("caf\u00e9\u009b"),
			want: "caf\u00e9" + `\xc2\x9b`,
		},
		{
			// The classes the pre-runesafe escaper forwarded verbatim
			// (Trojan-Source, CWE-117): a bidi override is well-formed 3-byte
			// UTF-8 and is NOT a C1 control, so only the shared predicate
			// catches it. An RLO in a filename visually reorders the rendered
			// log line in an operator terminal.
			name: "U+202E RLO bidi override is escaped",
			in:   []byte("evil\u202egnp.txt"),
			want: `evil\xe2\x80\xaegnp.txt`,
		},
		{
			name: "U+2066 LRI isolate is escaped",
			in:   []byte("a\u2066b"),
			want: `a\xe2\x81\xa6b`,
		},
		{
			// U+2028/U+2029 are line terminators to any consumer that splits
			// records on the Unicode class rather than bare \n; escaping them
			// keeps one visual line one record everywhere downstream.
			name: "U+2028 line separator is escaped",
			in:   []byte("x\u2028y"),
			want: `x\xe2\x80\xa8y`,
		},
		{
			name: "U+2029 paragraph separator is escaped",
			in:   []byte("x\u2029y"),
			want: `x\xe2\x80\xa9y`,
		},
		// The nearest sibling OUTSIDE each unsafe class stays verbatim: the
		// escape set must not creep into legitimate typography, and an
		// off-by-one in the class tables fails here, not seven codepoints out.
		{
			name: "U+202F narrow NBSP just past the U+202E bidi run is forwarded verbatim",
			in:   []byte("a\u202fb"),
			want: "a\u202fb",
		},
		{
			name: "U+2065 unassigned gap before the bidi isolates is forwarded verbatim",
			in:   []byte("a\u2065b"),
			want: "a\u2065b",
		},
		{
			name: "U+2030 per-mille just past U+2029 is forwarded verbatim",
			in:   []byte("50\u2030"),
			want: "50\u2030",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := string(sanitizeControlBytes(tc.in)); got != tc.want {
				t.Errorf("sanitizeControlBytes(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// FuzzSanitizeControlBytes pins the properties of the unexported sanitizer
// directly (Writer as a whole is not idempotent because of line
// framing, so idempotency can only be asserted on the function itself). The
// seed corpus runs deterministically every PR; the properties are asserted
// against the post-UTF-8-awareness behavior (a standalone C1 / invalid byte is
// escaped; valid multi-byte runes pass through verbatim EXCEPT the runes
// runesafe.IsUnsafeNonASCII refuses -- C1 controls, bidi controls, and
// U+2028/U+2029 -- whose UTF-8 bytes are escaped).
func FuzzSanitizeControlBytes(f *testing.F) {
	f.Add("clean printable text /scan/file.txt")
	f.Add("esc \x1b[31mred\x1b[0m reset")
	f.Add("nul\x00 and del\x7f bytes")
	f.Add("bidi \u202e override and isolate \u2066")
	f.Add("seps \u2028 and \u2029 forge lines")
	f.Add("tab\there newline\nkept")
	f.Add("valid multibyte caf\u00e9 \u65e5\u672c\u8a9e kept")
	f.Add("standalone C1 \x9b and invalid \xff escaped")
	f.Add("wellformed C1 \u009b escaped, nbsp \u00a0 and \u00e9 kept")
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
		// though it is neither a C0 control nor DEL -- it is escaped instead. A
		// well-formed rune in runesafe's unsafe non-ASCII classes (C1 controls,
		// bidi controls, U+2028/U+2029) is likewise NOT clean: each drives
		// terminal escapes or reorders/forges the rendered line, so the identity
		// guarantee holds only for lines free of them. The exclusion is computed
		// here via an independent rune scan against runesafe.IsUnsafeNonASCII
		// (not the production byte checks), so a discrepancy between the two is
		// caught rather than assumed away.
		clean := utf8.Valid([]byte(input))
		if clean {
			for _, b := range []byte(input) {
				if (b < 0x20 && b != '\n' && b != '\t') || b == 0x7f {
					clean = false
					break
				}
			}
		}
		if clean {
			for _, r := range input {
				if runesafe.IsUnsafeNonASCII(r) {
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
