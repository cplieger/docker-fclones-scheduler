package linefilter

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/cplieger/runesafe/v2"
)

// shouldFilterLine is unexported, so its tests live in this internal test
// file. FuzzShouldFilterLine re-declares the noise pattern lists as an
// independent oracle, so a wrong edit to either side is caught.

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
		// FIEMAP phrase echoed in an attacker-controlled filename inside a
		// genuine error line must NOT be suppressed (warn-level marker, wrong level).
		{"[2026-04-26 11:00:03.072] fclones: error: cannot read /scandir/doesn't support FIEMAP ioctl API.txt", false},
		// Same for an info marker echoed in an error-level line.
		{"[2026-04-26 11:00:03.072] fclones: error: cannot read /scandir/Found duplicates.txt", false},
		// A genuine warn ABOUT an attacker-named path that merely echoes the
		// FIEMAP phrase (framed "cannot read file", not "File system") must
		// survive: the prefix anchor distinguishes them.
		{"[2026-04-26 11:00:03.072] fclones: warn: cannot read file /scandir/x doesn't support FIEMAP ioctl API: permission denied", false},
		{"Scanned 100 file entries", false},
		{"[2026-04-26 11:00:03.072] fclones: error: Started grouping cache rebuild failed", false},
		{`time=... level=INFO msg="scan complete" scan_id=abc`, false},
		{"", false},
		// A warn-only marker as the genuine body of an info: line must survive:
		// markers are level-keyed.
		{"[2026-04-26 11:00:03.072] fclones:  info: doesn't support FIEMAP ioctl API today", false},
		{"[2026-04-26 11:00:03.072] fclones:  warn: Started grouping the cache", false},
		// fclones: prefix present but no second colon (malformed level).
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
	// Genuine drops at each registered level; the warn seed uses fclones'
	// canonical "File system " framing to satisfy the prefix anchor.
	f.Add("[ts] fclones:  info: Started grouping")
	f.Add("[ts] fclones: warn: File system zfs on device data/media doesn't support FIEMAP ioctl API. harmless.")
	// Same-level guard: a warn about an attacker-named path echoing the
	// FIEMAP phrase without "File system " framing must not drop.
	f.Add("[ts] fclones: warn: cannot read file /scandir/x doesn't support FIEMAP ioctl API: denied")
	// Cross-level: a marker echoed at the wrong level must survive.
	f.Add("[ts] fclones: error: cannot read /scandir/doesn't support FIEMAP ioctl API.txt")
	f.Add("[ts] fclones:  info: doesn't support FIEMAP ioctl API today")
	// fclones: prefix but no second colon (malformed level): not filtered.
	f.Add("[ts] fclones: warnnocolon doesn't support FIEMAP ioctl API")
	f.Fuzz(func(t *testing.T, input string) {
		// Independent oracle mirroring shouldFilterLine's positional policy.
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

// TestSanitizeControlBytesEscapesUnsafeRunes pins the well-formed-rune half
// of the escape class (runesafe.IsUnsafeNonASCII): the C1 control block, the
// Unicode bidi controls, and U+2028/U+2029 — each a CWE-117 vector on an
// attacker-influenced scanned filename. Boundaries are exact: the nearest
// sibling codepoint outside each class is forwarded verbatim. Calls
// sanitizeControlBytes directly so both the fast and slow paths are exercised.
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

// FuzzSanitizeControlBytes pins the sanitizer's properties directly (Writer
// as a whole is not idempotent because of line framing). Seeds run
// deterministically every PR.
func FuzzSanitizeControlBytes(f *testing.F) {
	f.Add("clean printable text /scan/file.txt")
	f.Add("esc \x1b[31mred\x1b[0m reset")
	f.Add("nul\x00 and del\x7f bytes")
	f.Add("bidi \u202e override and isolate \u2066")
	f.Add("seps \u2028 and \u2029 forge lines")
	f.Add("tab\there newline\nkept")
	f.Add("valid multibyte caf\u00e9 \u65e5\u672c\u8a9e kept")
	f.Add("standalone C1 \x9b and invalid \xff escaped")
	f.Add("lone continuation byte \x80 at the ASCII edge")
	f.Add("wellformed C1 \u009b escaped, nbsp \u00a0 and \u00e9 kept")
	f.Add("")
	f.Fuzz(func(t *testing.T, input string) {
		out := sanitizeControlBytes([]byte(input))

		// Efficacy (CWE-117): no forbidden control byte survives.
		for _, b := range out {
			if (b < 0x20 && b != '\n' && b != '\t') || b == 0x7f {
				t.Fatalf("forbidden control byte %#02x survived in %q for input %q", b, out, input)
			}
		}

		// Well-formedness: the sanitized line is valid UTF-8.
		if !utf8.Valid(out) {
			t.Fatalf("sanitize(%q) = %q, which is not valid UTF-8", input, out)
		}

		// Idempotency: a second pass takes the fast path and changes nothing.
		if again := sanitizeControlBytes(out); string(again) != string(out) {
			t.Fatalf("not idempotent: sanitize(%q) = %q, second pass = %q", input, out, again)
		}

		// Bounded transform: escaping (1 byte -> 4 bytes) is the only growth.
		if len(out) < len(input) || len(out) > 4*len(input) {
			t.Fatalf("output length %d outside [%d, %d] for input %q", len(out), len(input), 4*len(input), input)
		}

		// Identity fast path: clean input (valid UTF-8, no forbidden control
		// byte, no unsafe rune) is returned verbatim. Computed via an
		// independent scan against runesafe.IsUnsafeNonASCII, not the
		// production byte checks, so a discrepancy is caught rather than assumed.
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
