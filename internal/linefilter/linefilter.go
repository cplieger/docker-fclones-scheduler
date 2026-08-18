// Package linefilter provides the line-oriented stderr filter for fclones
// subprocess output: per-line noise filtering, control-byte escaping (CWE-117),
// and a bounded partial-line buffer with flood force-flush.
package linefilter

import (
	"bytes"
	"io"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/cplieger/runesafe/v2"
)

// MaxLineBytes bounds the partial-line buffer in Writer so a
// no-newline byte flood cannot grow it without limit. It is the threshold a
// flood force-flush trips (see Floods), so the caller logs it directly rather
// than a coincidentally-equal config constant. It mirrors the 1 MB stderr cap
// the caller applies to its capbuf.Buffer sink.
const MaxLineBytes = 1 << 20 // 1 MB

// Writer wraps an io.Writer and drops lines matching known-recurring
// noise from upstream fclones.
type Writer struct {
	w      io.Writer
	buf    []byte
	floods int
}

// New returns a line-filtering wrapper around w.
func New(w io.Writer) *Writer {
	return &Writer{w: w}
}

// noiseMarker is a body substring that marks noise at its level. An optional
// prefix, when set, additionally requires the message body to begin with
// fclones' own framing for that notice, so a marker echoed in an attacker-named
// scanned path (which fclones prefixes with "cannot read file ", never with the
// notice's own framing) cannot suppress a genuine same-level diagnostic.
type noiseMarker struct {
	substr string
	prefix string // "" means no prefix anchor
}

// infoProgressPatterns mark info-level progress noise (matched against the
// message body of a genuine fclones info line; see shouldFilterLine). These
// carry no prefix anchor: the phrases are fclones-controlled framing already.
var infoProgressPatterns = []noiseMarker{
	{substr: "Started grouping"},
	{substr: "Started deduplicating"},
	{substr: "Scanned "},
	{substr: "Found "},
}

// warnNoisePatterns mark warn-level noise. The FIEMAP notice ("doesn't support
// FIEMAP ioctl API", e.g. on ZFS) is benign and recurrent and
// is emitted by fclones at warn level. It is matched against the message body
// of a genuine fclones warn line -- not anywhere in the raw line -- AND anchored
// to fclones' own framing for the canonical notice ("File system <fs> on device
// <dev> doesn't support FIEMAP ioctl API ..."). fclones prefixes path read
// errors with "cannot read file ", never with "File system ", so a genuine
// same-level warn that merely echoes the phrase in an attacker-named scanned
// filename is NOT suppressed.
var warnNoisePatterns = []noiseMarker{
	{substr: "doesn't support FIEMAP ioctl API", prefix: "File system "},
}

// noisePatternsByLevel maps an fclones log level to the body markers dropped at
// that level. A marker is matched ONLY against the message body of a line whose
// level field equals the key (see shouldFilterLine), never against the raw
// line, so an attacker-controlled scanned filename echoing a marker cannot
// suppress the line that reports it. Re-audit on a FCLONES_VERSION bump (see
// CONTRIBUTING.md).
var noisePatternsByLevel = map[string][]noiseMarker{
	"info": infoProgressPatterns,
	"warn": warnNoisePatterns,
}

// shouldFilterLine returns true when a given line should be suppressed.
//
// It reads the level positionally from the FIRST "fclones:" prefix that fclones
// itself emits (an attacker cannot inject text ahead of it), then drops the line
// only when a noise marker registered for that exact level appears in the
// message body. A marker carrying a prefix anchor additionally requires the body
// to begin with fclones' own framing for that notice, so neither a marker echoed
// in a scanned filename at a different level NOR one echoed in a genuine
// same-level diagnostic about an attacker-named path can suppress it.
func shouldFilterLine(line string) bool {
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
	patterns, ok := noisePatternsByLevel[strings.TrimSpace(level)]
	if !ok {
		return false
	}
	// fclones renders the body with a leading space after the level colon
	// ("warn: File system ..."); trim it so a marker's prefix anchor matches the
	// body's own framing rather than that separator space. Trimming is
	// behavior-preserving for the substring check (a leading space is never part
	// of a marker phrase).
	body := strings.TrimLeft(msg, " ")
	for _, m := range patterns {
		if m.prefix != "" && !strings.HasPrefix(body, m.prefix) {
			continue
		}
		if strings.Contains(body, m.substr) {
			return true
		}
	}
	return false
}

// hexDigits indexes the lowercase hex alphabet for the "\xNN" escape forms
// produced by escHexByte.
const hexDigits = "0123456789abcdef"

// isC0OrDEL reports whether b is a C0 control byte (0x00-0x1F) other than the
// framing-significant '\n' and the benign separator '\t', or DEL (0x7F) -- the
// single bytes sanitizeControlBytes rewrites to a visible escape.
func isC0OrDEL(b byte) bool {
	return (b < 0x20 && b != '\n' && b != '\t') || b == 0x7f
}

// containsUnsafeRune reports whether p (assumed valid UTF-8 by its caller) holds
// any rune runesafe.IsUnsafeNonASCII refuses, so an unsafe multi-byte rune
// cannot slip through the fast path verbatim.
//
// The predicate is runesafe's, not a local list. This function used to look for
// the 2-byte C1 encoding only, which meant the two classes above the C1 block --
// the Unicode bidi controls and U+2028/U+2029 -- were well-formed UTF-8 with
// nothing to escape, took the fast path, and reached an operator terminal and
// Loki verbatim (Trojan-Source reordering and forged log records on an
// attacker-influenced filename). Deferring the class definition to the shared
// predicate is what keeps that gap from reopening.
func containsUnsafeRune(p []byte) bool {
	// Decode in place rather than `range string(p)`: the conversion heap-copies
	// the line, and this guard runs on EVERY clean line (and on the flood
	// force-flush path, where the copy would be a transient >1 MB allocation on
	// the path whose purpose is bounding memory). The caller guarantees valid
	// UTF-8, and IsUnsafeNonASCII is false for ASCII and RuneError alike, so a
	// plain DecodeRune walk is byte-for-byte equivalent.
	for i := 0; i < len(p); {
		r, size := utf8.DecodeRune(p[i:])
		if runesafe.IsUnsafeNonASCII(r) {
			return true
		}
		i += size
	}
	return false
}

// escHexByte appends the visible "\xNN" escape of b to dst and returns the
// extended slice.
func escHexByte(dst []byte, b byte) []byte {
	return append(dst, '\\', 'x', hexDigits[b>>4], hexDigits[b&0x0f])
}

// escapeControlBytes is the allocating slow path of sanitizeControlBytes. It
// rewrites each C0/DEL byte, each byte that cannot form a valid rune (incl. a
// bare 8-bit C1 control), and the UTF-8 bytes of each well-formed rune
// runesafe.IsUnsafeNonASCII refuses (C1 controls, bidi controls, U+2028/U+2029)
// to a visible "\xNN" escape, while forwarding every other valid rune verbatim.
func escapeControlBytes(line []byte) []byte {
	out := make([]byte, 0, len(line))
	for i := 0; i < len(line); {
		b := line[i]
		if isC0OrDEL(b) {
			out = escHexByte(out, b)
			i++
			continue
		}
		if b < utf8.RuneSelf { // printable ASCII plus the exempt '\n', '\t'
			out = append(out, b)
			i++
			continue
		}
		// b >= 0x80: decode the rune. A standalone/invalid byte and any rune
		// runesafe.IsUnsafeNonASCII refuses -- a C1 control (drives terminal
		// escapes despite being valid UTF-8), a Unicode bidi control (reorders
		// the rendered line), U+2028/U+2029 (line terminators a JSON consumer
		// may split on) -- are escaped byte-by-byte; every other valid
		// multi-byte rune is forwarded verbatim.
		r, size := utf8.DecodeRune(line[i:])
		if (r == utf8.RuneError && size == 1) || runesafe.IsUnsafeNonASCII(r) {
			for j := range size {
				out = escHexByte(out, line[i+j])
			}
			i += size
			continue
		}
		out = append(out, line[i:i+size]...)
		i += size
	}
	return out
}

// sanitizeControlBytes neutralizes, by rewriting each byte to a visible "\xNN"
// escape before the line is forwarded to the log sink: C0 control bytes
// (0x00-0x1F) other than '\n' and '\t', DEL (0x7F), any byte that is not part
// of a valid UTF-8 sequence (which includes a standalone C1 control such as
// the bare 8-bit CSI 0x9B), and the UTF-8 bytes of every well-formed rune
// runesafe.IsUnsafeNonASCII refuses: the C1 block U+0080..U+009F (terminal
// escape introducers even in their 2-byte form, e.g. U+009B = 0xC2 0x9B), the
// Unicode bidi controls (a filename embedding an RLO visually reorders the
// rendered log line -- Trojan-Source), and U+2028/U+2029 (line terminators a
// downstream viewer may split records on). The class definition is runesafe's,
// shared across the fleet, so this sink cannot drift from the fleet policy;
// the \xNN output shape is this package's (space-replacement would destroy
// the forensic record of WHICH bytes arrived).
//
// fclones renders scanned filenames raw into its stderr diagnostics (e.g.
// "cannot read file <path>"), and <path> is attacker-influenceable: without
// this rewrite those runes would reach an operator's terminal or Loki
// unescaped (CWE-117 log injection). '\n' is left intact so the line-oriented
// framing is preserved (Write has already split on it, so a line reaching emit
// holds at most a single trailing '\n'); '\t' is a benign, common separator.
// Every other valid multi-byte UTF-8 rune (accented Latin such as U+00E9,
// NBSP U+00A0, CJK, emoji) is forwarded verbatim so legitimate non-ASCII
// filenames survive. The input is returned unchanged, without allocating, when
// it is already valid UTF-8 with nothing to escape (the common case), so
// well-formed fclones output pays no copy.
func sanitizeControlBytes(line []byte) []byte {
	// Fast path: valid UTF-8 with no C0/DEL control and no unsafe non-ASCII
	// rune -> forward verbatim, no alloc.
	if utf8.Valid(line) && !slices.ContainsFunc(line, isC0OrDEL) && !containsUnsafeRune(line) {
		return line
	}
	return escapeControlBytes(line)
}

// EscapeUnsafe applies sanitizeControlBytes' exact policy to an arbitrary
// string for sinks OUTSIDE the line-filtered stderr passthrough -- today the
// slog attributes that carry a failed subprocess's captured stderr/stdout
// tails. Those captures hold the same attacker-influenceable filenames the
// passthrough escapes, and a JSON log handler escapes C0 controls but forwards
// bidi controls and U+2028/U+2029 verbatim, so the rendered Loki line is
// reorderable without this (same CWE-117 vector, different sink). One exported
// entry point keeps both sinks on one policy: the class is runesafe's, the
// forensic \xNN output shape is this package's. '\n' and '\t' stay intact
// here too -- a multi-line stderr tail keeps its framing inside the quoted
// attribute value.
func EscapeUnsafe(s string) string {
	return string(sanitizeControlBytes([]byte(s)))
}

// emit applies the noise filter to a single line and writes it through to the
// wrapped writer unless it is filtered, sanitizing control bytes first (see
// sanitizeControlBytes) so control characters in attacker-named scanned paths
// cannot inject terminal escapes or forge log content. It is the single
// filter-then-write step shared by Write's per-line and flood-flush paths and
// by Flush.
func (fw *Writer) emit(line []byte) error {
	if shouldFilterLine(string(line)) {
		return nil
	}
	_, err := fw.w.Write(sanitizeControlBytes(line))
	return err
}

// Write implements io.Writer with line-oriented filtering.
func (fw *Writer) Write(p []byte) (int, error) {
	buf := fw.buf
	buf = append(buf, p...)
	fw.buf = nil

	for {
		idx := bytes.IndexByte(buf, '\n')
		if idx < 0 {
			// Copy the unconsumed tail into a fresh buffer rather than aliasing
			// buf (fw.buf = buf): earlier iterations may have re-sliced buf past
			// already-emitted lines, so aliasing would pin their backing array.
			// fw.buf is nil here, so append allocates a tight copy of just the tail.
			fw.buf = append(fw.buf, buf...)
			if len(fw.buf) > MaxLineBytes {
				fw.floods++
				err := fw.emit(fw.buf)
				fw.buf = nil
				if err != nil {
					return len(p), err
				}
			}
			break
		}
		line := buf[:idx+1]
		if err := fw.emit(line); err != nil {
			return len(p), err
		}
		buf = buf[idx+1:]
	}
	return len(p), nil
}

// Flush writes any remaining buffered partial line (applying the filter)
// and resets the buffer. Call after the subprocess exits to avoid losing
// the final line if it lacks a trailing newline.
func (fw *Writer) Flush() error {
	if len(fw.buf) == 0 {
		return nil
	}
	line := fw.buf
	fw.buf = nil
	return fw.emit(line)
}

// Floods reports how many times the partial-line buffer exceeded MaxLineBytes
// and was force-flushed (a no-newline output flood). It mirrors the visibility
// capbuf.Buffer.Total/Truncated give their cap: the caller logs a non-zero
// count so the otherwise-silent flood bound is observable in Loki/Grafana.
func (fw *Writer) Floods() int { return fw.floods }
