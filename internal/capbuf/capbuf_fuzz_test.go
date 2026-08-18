package capbuf_test

import (
	"testing"

	"github.com/cplieger/docker-fclones-scheduler/internal/capbuf"
)

func FuzzBuffer(f *testing.F) {
	f.Add([]byte("hello world"), 5)
	f.Add([]byte("short"), 100)
	f.Add([]byte(""), 0)
	f.Add([]byte("exactly"), 7)
	f.Add([]byte("negative cap"), -5)
	f.Fuzz(func(t *testing.T, data []byte, max int) {
		lb := &capbuf.Buffer{Max: max}
		n, err := lb.Write(data)
		if err != nil {
			t.Fatalf("Write should never error, got: %v", err)
		}
		if n != len(data) {
			t.Fatalf("Write returned %d, want %d", n, len(data))
		}
		// A non-positive Max clamps to an effective cap of 0 (the
		// max(0, b.Max-b.buf.Len()) guard in Write), so the buffer content
		// must never exceed max(0, max).
		effCap := max
		if effCap < 0 {
			effCap = 0
		}
		if len(lb.String()) > effCap {
			t.Fatalf("buffer len %d exceeds effective cap %d", len(lb.String()), effCap)
		}
		// Total must equal bytes written
		if lb.Total() != len(data) {
			t.Fatalf("Total() = %d, want %d", lb.Total(), len(data))
		}
		// Truncated iff wrote more than max
		if lb.Truncated() != (len(data) > max) {
			t.Fatalf("Truncated() = %v, want %v", lb.Truncated(), len(data) > max)
		}
	})
}
