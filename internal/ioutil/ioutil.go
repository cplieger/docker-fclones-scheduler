// Package ioutil provides bounded I/O helpers for capturing fclones
// subprocess output without unbounded memory growth: a line-filtering
// writer, a capped accumulation buffer, and a size-limited file reader.
package ioutil

import (
	"bytes"
	"fmt"
	"io"
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

// filteredPatterns lists all substrings that mark a line as noise to suppress.
var filteredPatterns = []string{
	"doesn't support FIEMAP ioctl API",
	"info: Started grouping",
	"info: Started deduplicating",
	"info: Scanned ",
	"info: Found ",
}

// shouldFilterLine returns true when a given line should be suppressed.
func shouldFilterLine(line string) bool {
	for _, p := range filteredPatterns {
		if strings.Contains(line, p) {
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
	// grew between the Stat check above and this read.
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("file %s grew past %d byte limit during read", path, maxBytes)
	}
	return data, nil
}
