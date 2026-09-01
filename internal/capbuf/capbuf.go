// Package capbuf provides a capped accumulation buffer for subprocess stream
// capture: writes above the cap are counted but not stored, so a runaway
// stream cannot grow memory while the caller still learns the true size.
package capbuf

import "bytes"

// Buffer is a bytes.Buffer that stops accumulating after Max bytes.
type Buffer struct {
	buf     bytes.Buffer
	Max     int
	totalIn int
}

// Write implements io.Writer with bounded accumulation. It always reports
// the full input length as written while storing at most the bytes that
// still fit under Max; a buffer already at Max stores nothing further.
func (b *Buffer) Write(p []byte) (int, error) {
	b.totalIn += len(p)
	room := max(0, b.Max-b.buf.Len())
	b.buf.Write(p[:min(len(p), room)])
	return len(p), nil
}

// String returns the accumulated buffer content.
func (b *Buffer) String() string {
	return b.buf.String()
}

// Total returns the sum of all bytes passed to Write.
func (b *Buffer) Total() int { return b.totalIn }

// Truncated reports whether Write ever discarded bytes.
func (b *Buffer) Truncated() bool { return b.totalIn > b.Max }
