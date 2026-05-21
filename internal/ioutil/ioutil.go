package ioutil

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
)

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

// ShouldFilterLine returns true when a given line should be suppressed.
func ShouldFilterLine(line string) bool {
	for _, p := range filteredPatterns {
		if strings.Contains(line, p) {
			return true
		}
	}
	return false
}

// Write implements io.Writer with line-oriented filtering.
func (fw *FilteringWriter) Write(p []byte) (int, error) {
	buf := fw.buf
	buf = append(buf, p...)
	fw.buf = nil

	for {
		idx := bytes.IndexByte(buf, '\n')
		if idx < 0 {
			fw.buf = append(fw.buf, buf...)
			break
		}
		line := buf[:idx+1]
		if !ShouldFilterLine(string(line)) {
			if _, err := fw.w.Write(line); err != nil {
				return len(p), err
			}
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
	line := string(fw.buf)
	fw.buf = nil
	if !ShouldFilterLine(line) {
		_, err := fw.w.Write([]byte(line))
		return err
	}
	return nil
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

// Write implements io.Writer with bounded accumulation.
func (lb *LimitedBuffer) Write(p []byte) (int, error) {
	lb.totalIn += len(p)
	if lb.buf.Len() >= lb.Max {
		return len(p), nil
	}
	remaining := lb.Max - lb.buf.Len()
	if len(p) > remaining {
		lb.buf.Write(p[:remaining])
		return len(p), nil
	}
	return lb.buf.Write(p)
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

	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("file %s grew past %d byte limit during read", path, maxBytes)
	}
	return data, nil
}
