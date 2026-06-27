// Package ioutil provides bounded I/O helpers for capturing fclones
// subprocess output without unbounded memory growth: a line-filtering
// writer, a capped accumulation buffer, and a size-limited file reader.
package ioutil

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
)

// maxLineBytes bounds the partial-line buffer in FilteringWriter so a
// no-newline byte flood cannot grow it without limit. It mirrors the 1 MB
// stderr cap applied to the sibling LimitedBuffer sink.
const maxLineBytes = 1 << 20 // 1 MB

// FilteringWriter wraps an io.Writer and drops lines matching known-recurring
// noise from upstream fclones.
type FilteringWriter struct {
	w   io.Writer
	buf []byte
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

// emit applies the noise filter to a single line and writes it through to the
// wrapped writer unless it is filtered. It is the single filter-then-write step
// shared by Write's per-line and flood-flush paths and by Flush.
func (fw *FilteringWriter) emit(line []byte) error {
	if shouldFilterLine(string(line)) {
		return nil
	}
	_, err := fw.w.Write(line)
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
			if len(fw.buf) > maxLineBytes {
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

// Close implements io.Closer by flushing the remaining buffer.
func (fw *FilteringWriter) Close() error {
	return fw.Flush()
}

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

// ReadFileWithLimit reads a file up to maxBytes. Returns an error if the file
// exceeds the limit or cannot be read.
func ReadFileWithLimit(path string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("file %s is %d bytes, exceeds %d byte limit", path, info.Size(), maxBytes)
	}

	// Read one byte past the limit (maxBytes+1) so an oversized file is detected
	// rather than silently truncated to maxBytes. The length re-check below turns
	// that extra byte into an error and also closes the TOCTOU gap where the file
	// grew between the Stat check above and this read. Guard the +1 against int64
	// overflow: at maxBytes == math.MaxInt64 the +1 would wrap to a negative value,
	// which io.LimitReader treats as immediate EOF (silently returning empty data).
	// No file can exceed that cap anyway (Stat already rejected info.Size() >
	// maxBytes, and a file cannot exceed MaxInt64 bytes), so read up to MaxInt64
	// without the extra byte.
	limit := maxBytes
	if limit < math.MaxInt64 {
		limit++ // read one past the cap to detect oversize / TOCTOU growth
	}
	data, err := io.ReadAll(io.LimitReader(f, limit))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("file %s grew past %d byte limit during read", path, maxBytes)
	}
	return data, nil
}
