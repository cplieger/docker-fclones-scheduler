package ioutil_test

import (
	"testing"

	"github.com/cplieger/fclones-wrapper/internal/ioutil"
)

func FuzzLimitedBuffer(f *testing.F) {
	f.Add([]byte("hello world"), 5)
	f.Add([]byte("short"), 100)
	f.Add([]byte(""), 0)
	f.Add([]byte("exactly"), 7)
	f.Add([]byte("negative cap"), -5)
	f.Fuzz(func(t *testing.T, data []byte, max int) {
		lb := &ioutil.LimitedBuffer{Max: max}
		n, err := lb.Write(data)
		if err != nil {
			t.Fatalf("Write should never error, got: %v", err)
		}
		if n != len(data) {
			t.Fatalf("Write returned %d, want %d", n, len(data))
		}
		// A non-positive Max clamps to an effective cap of 0 (the
		// max(0, lb.Max-lb.buf.Len()) guard in Write), so the buffer content
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

func FuzzFilteringWriter(f *testing.F) {
	f.Add("info: Started grouping\nkeep me\n")
	f.Add("no newline at all")
	f.Add("")
	f.Add("\n\n\n")
	f.Add("info: Scanned 5\npartial line")
	f.Fuzz(func(t *testing.T, input string) {
		sink := &lenSink{}
		fw := ioutil.NewFilteringWriter(sink)

		n, err := fw.Write([]byte(input))
		if err != nil {
			t.Fatalf("Write(%q) into a counting sink returned error %v, want nil", input, err)
		}
		if n != len(input) {
			t.Fatalf("Write(%q) returned n=%d, want %d (full input length)", input, n, len(input))
		}
		if err := fw.Flush(); err != nil {
			t.Fatalf("Flush after Write(%q) returned error %v, want nil", input, err)
		}

		// FilteringWriter only ever drops whole lines, so it must never emit
		// more bytes than it received -- regardless of how lines split across
		// the internal buffer or the no-newline cap-flush path.
		if sink.n > len(input) {
			t.Fatalf("filtered output %d bytes exceeds input %d bytes for %q", sink.n, len(input), input)
		}
	})
}

type lenSink struct{ n int }

func (s *lenSink) Write(p []byte) (int, error) {
	s.n += len(p)
	return len(p), nil
}
