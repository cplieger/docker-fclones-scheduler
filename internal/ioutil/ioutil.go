// Package ioutil provides bounded I/O helpers for capturing fclones
// subprocess output without unbounded memory growth: a line-filtering
// writer and a capped accumulation buffer.
package ioutil

import (
	"bytes"
	"io"
	"slices"
	"strings"
	"unicode/utf8"
)

// MaxLineBytes bounds the partial-line buffer in FilteringWriter so a
// no-newline byte flood cannot grow it without limit. It is the threshold a
// flood force-flush trips (see Floods), so the caller logs it directly rather
// than a coincidentally-equal config constant. It mirrors the 1 MB stderr cap
// applied to the sibling LimitedBuffer sink.
const MaxLineBytes = 1 << 20 // 1 MB

// FilteringWriter wraps an io.Writer and drops lines matching known-recurring
// noise from upstream fclones.
type FilteringWriter struct {
	w      io.Writer
	buf    []byte
	floods int
}

// NewFilteringWriter returns a line-filtering wrapper around w.
func NewFilteringWriter(w io.Writer) *FilteringWriter {
	return &FilteringWriter{w: w}
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

// isC1Control reports whether r is in the C1 control block U+0080..U+009F
// (category-Cc controls that drive terminal escape sequences). NBSP U+00A0 and
// every higher rune are excluded, so they are forwarded verbatim.
func isC1Control(r rune) bool {
	return r >= 0x80 && r <= 0x9f
}

// containsC1Rune reports whether p (assumed valid UTF-8 by its caller) holds the
// 2-byte encoding of any C1 control U+0080..U+009F. In valid UTF-8 that is
// always 0xC2 followed by a continuation byte in [0x80,0x9F]; 0xC2 with a
// [0xA0,0xBF] continuation is U+00A0..U+00BF (NBSP and other Latin-1 supplement)
// and is left alone. It keeps a well-formed C1 rune from slipping through the
// fast path verbatim.
func containsC1Rune(p []byte) bool {
	for i := 0; i+1 < len(p); i++ {
		if p[i] == 0xc2 && p[i+1] >= 0x80 && p[i+1] <= 0x9f {
			return true
		}
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
// bare 8-bit C1 control), and each well-formed C1 control U+0080..U+009F to a
// visible "\xNN" escape, while forwarding every other valid rune verbatim.
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
		// b >= 0x80: decode the rune. A standalone/invalid byte (incl. a bare C1
		// control) and a well-formed C1 control U+0080..U+009F are both escaped
		// byte-by-byte (the latter drives terminal escapes despite being valid
		// UTF-8); every other valid multi-byte rune is forwarded verbatim.
		r, size := utf8.DecodeRune(line[i:])
		if (r == utf8.RuneError && size == 1) || isC1Control(r) {
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

// sanitizeControlBytes neutralizes C0 control bytes (0x00-0x1F) other than
// '\n' and '\t', DEL (0x7F), the C1 control block U+0080..U+009F (Unicode
// category-Cc controls that drive terminal escape sequences -- escaped even
// when they arrive as their well-formed 2-byte UTF-8 encoding 0xC2 0x80..0x9F,
// e.g. the 8-bit CSI U+009B = 0xC2 0x9B), and any byte that is not part of a
// valid UTF-8 sequence (which includes a standalone C1 control such as the bare
// 8-bit CSI 0x9B), by rewriting each to a visible "\xNN" escape before the line
// is forwarded to the log sink. fclones renders scanned filenames raw into its
// stderr diagnostics (e.g. "cannot read file <path>"), and <path> is
// attacker-influenceable: a file whose name embeds ANSI escape sequences (ESC,
// 0x1B), a carriage return, a NUL, or a C1 CSI (as the raw byte 0x9B OR its
// valid UTF-8 form 0xC2 0x9B) would otherwise reach an operator's terminal or
// Loki unescaped (CWE-117 log injection). '\n' is left intact so the
// line-oriented framing is preserved (Write has already split on it, so a line
// reaching emit holds at most a single trailing '\n'); '\t' is a benign, common
// separator. Every other valid multi-byte UTF-8 rune (accented Latin such as
// U+00E9, NBSP U+00A0, CJK, emoji) is forwarded verbatim so legitimate
// non-ASCII filenames survive; only C0/DEL bytes, C1 controls, and bytes that
// cannot form a valid rune are escaped. The input is returned unchanged, without
// allocating, when it is already valid UTF-8 with nothing to escape (the common
// case), so well-formed fclones output pays no copy.
func sanitizeControlBytes(line []byte) []byte {
	// Fast path: valid UTF-8 with no C0/DEL control and no well-formed C1 rune
	// -> forward verbatim, no alloc.
	if utf8.Valid(line) && !slices.ContainsFunc(line, isC0OrDEL) && !containsC1Rune(line) {
		return line
	}
	return escapeControlBytes(line)
}

// emit applies the noise filter to a single line and writes it through to the
// wrapped writer unless it is filtered, sanitizing control bytes first (see
// sanitizeControlBytes) so control characters in attacker-named scanned paths
// cannot inject terminal escapes or forge log content. It is the single
// filter-then-write step shared by Write's per-line and flood-flush paths and
// by Flush.
func (fw *FilteringWriter) emit(line []byte) error {
	if shouldFilterLine(string(line)) {
		return nil
	}
	_, err := fw.w.Write(sanitizeControlBytes(line))
	return err
}

// Write implements io.Writer with line-oriented filtering.
func (fw *FilteringWriter) Write(p []byte) (int, error) {
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
func (fw *FilteringWriter) Flush() error {
	if len(fw.buf) == 0 {
		return nil
	}
	line := fw.buf
	fw.buf = nil
	return fw.emit(line)
}

// Floods reports how many times the partial-line buffer exceeded MaxLineBytes
// and was force-flushed (a no-newline output flood). It mirrors the visibility
// LimitedBuffer.Total/Truncated give their cap: the caller logs a non-zero
// count so the otherwise-silent flood bound is observable in Loki/Grafana.
func (fw *FilteringWriter) Floods() int { return fw.floods }

// LimitedBuffer is a bytes.Buffer that stops accumulating after Max bytes.
type LimitedBuffer struct {
	buf     bytes.Buffer
	Max     int
	totalIn int
}

// Write implements io.Writer with bounded accumulation. It always reports the
// full input length as written (the buffer is a capped sink that never errors)
// while storing at most the bytes that still fit under Max. The available room
// is clamped to a non-negative value, so a buffer already at Max -- or a Max
// lowered below the current length -- stores nothing.
func (lb *LimitedBuffer) Write(p []byte) (int, error) {
	lb.totalIn += len(p)
	room := max(0, lb.Max-lb.buf.Len())
	lb.buf.Write(p[:min(len(p), room)])
	return len(p), nil
}

// String returns the accumulated buffer content.
func (lb *LimitedBuffer) String() string {
	return lb.buf.String()
}

// Total returns the sum of all bytes passed to Write.
func (lb *LimitedBuffer) Total() int { return lb.totalIn }

// Truncated reports whether Write ever discarded bytes.
func (lb *LimitedBuffer) Truncated() bool { return lb.totalIn > lb.Max }
