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

// MaxLineBytes bounds the partial-line buffer so a no-newline byte flood
// cannot grow it without limit; mirrors the 1 MB stderr cap on the caller's
// capbuf.Buffer sink.
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
// prefix additionally requires the message body to begin with fclones' own
// framing for that notice, so a marker echoed in an attacker-named scanned
// path cannot suppress a genuine same-level diagnostic.
type noiseMarker struct {
	substr string
	prefix string // "" means no prefix anchor
}

// infoProgressPatterns mark info-level progress noise; no prefix anchor
// needed since these phrases are fclones-controlled framing already.
var infoProgressPatterns = []noiseMarker{
	{substr: "Started grouping"},
	{substr: "Started deduplicating"},
	{substr: "Scanned "},
	{substr: "Found "},
}

// warnNoisePatterns mark warn-level noise. The FIEMAP notice (e.g. on ZFS) is
// benign and recurrent; anchored to fclones' own framing ("File system <fs>
// on device <dev> doesn't support FIEMAP ioctl API ...") so a genuine warn
// that merely echoes the phrase in an attacker-named scanned filename (which
// fclones instead prefixes "cannot read file ") is not suppressed.
var warnNoisePatterns = []noiseMarker{
	{substr: "doesn't support FIEMAP ioctl API", prefix: "File system "},
}

// noisePatternsByLevel maps an fclones log level to the body markers dropped
// at that level. Matched only against the message body of a line whose level
// field equals the key, never the raw line, so an attacker-controlled
// filename cannot suppress the line reporting it. Re-audit on a
// FCLONES_VERSION bump.
var noisePatternsByLevel = map[string][]noiseMarker{
	"info": infoProgressPatterns,
	"warn": warnNoisePatterns,
}

// shouldFilterLine returns true when a given line should be suppressed. It
// reads the level positionally from fclones' own "fclones:" prefix (an
// attacker cannot inject text ahead of it), then drops the line only when a
// noise marker registered for that exact level appears in the message body.
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
	// ("warn: File system ..."); trim it so a prefix anchor matches the
	// body's own framing rather than that separator space.
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

// containsUnsafeRune reports whether p (assumed valid UTF-8 by its caller)
// holds any rune runesafe.IsUnsafeNonASCII refuses (the Unicode bidi controls
// and U+2028/U+2029 are well-formed UTF-8 with nothing else to catch them —
// deferring to the shared predicate is what keeps that gap from reopening).
func containsUnsafeRune(p []byte) bool {
	// Decode in place rather than `range string(p)`: the conversion would
	// heap-copy the line on every call, including the flood force-flush path
	// whose purpose is bounding memory.
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

// escapeControlBytes is the allocating slow path of sanitizeControlBytes: it
// rewrites each C0/DEL byte, each invalid-UTF-8 byte, and the bytes of each
// rune runesafe.IsUnsafeNonASCII refuses to a visible "\xNN" escape, while
// forwarding every other valid rune verbatim.
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
		// A standalone/invalid byte, or a rune runesafe.IsUnsafeNonASCII
		// refuses (a C1 control, a bidi control, U+2028/U+2029), is escaped
		// byte-by-byte; every other valid multi-byte rune is forwarded as-is.
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

// sanitizeControlBytes rewrites, to a visible "\xNN" escape, every C0
// control byte (0x00-0x1F) except '\n'/'\t', DEL (0x7F), any byte not part of
// a valid UTF-8 sequence, and every rune runesafe.IsUnsafeNonASCII refuses
// (the C1 block, Unicode bidi controls, U+2028/U+2029). fclones renders
// attacker-influenceable scanned filenames raw into its stderr diagnostics;
// without this rewrite those bytes would reach an operator's terminal or
// Loki unescaped (CWE-117 log injection / Trojan-Source). Every other valid
// multi-byte rune (accented Latin, CJK, emoji) is forwarded verbatim. Returns
// the input unchanged, without allocating, when it is already clean.
func sanitizeControlBytes(line []byte) []byte {
	// Fast path: valid UTF-8 with no C0/DEL control and no unsafe non-ASCII
	// rune -> forward verbatim, no alloc.
	if utf8.Valid(line) && !slices.ContainsFunc(line, isC0OrDEL) && !containsUnsafeRune(line) {
		return line
	}
	return escapeControlBytes(line)
}

// EscapeUnsafe applies sanitizeControlBytes' exact policy to an arbitrary
// string, for sinks outside the line-filtered stderr passthrough — the slog
// attributes carrying a failed subprocess's captured stderr/stdout tails,
// which hold the same attacker-influenceable filenames but where a JSON log
// handler alone would forward bidi controls and U+2028/U+2029 verbatim.
func EscapeUnsafe(s string) string {
	return string(sanitizeControlBytes([]byte(s)))
}

// emit applies the noise filter to a single line and writes it through
// unless filtered, sanitizing control bytes first so attacker-named scanned
// paths cannot inject terminal escapes or forge log content.
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
			// Copy the unconsumed tail into a fresh buffer rather than
			// aliasing buf: earlier iterations may have re-sliced buf past
			// already-emitted lines, which would pin their backing array.
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

// Floods reports how many times the partial-line buffer exceeded
// MaxLineBytes and was force-flushed, so the caller can log a non-zero count
// making the otherwise-silent flood bound observable.
func (fw *Writer) Floods() int { return fw.floods }
