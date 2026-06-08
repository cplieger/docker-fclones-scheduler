package ioutil_test

import (
	"strings"
	"testing"

	"github.com/cplieger/fclones-wrapper/internal/ioutil"
)

func FuzzShouldFilterLine(f *testing.F) {
	f.Add("info: Started grouping files")
	f.Add("info: Scanned 100 files")
	f.Add("doesn't support FIEMAP ioctl API")
	f.Add("normal output line")
	f.Add("")
	f.Fuzz(func(t *testing.T, input string) {
		result := ioutil.ShouldFilterLine(input)
		// If result is true, input must contain one of the known patterns
		if result {
			patterns := []string{
				"doesn't support FIEMAP ioctl API",
				"info: Started grouping",
				"info: Started deduplicating",
				"info: Scanned ",
				"info: Found ",
			}
			found := false
			for _, p := range patterns {
				if strings.Contains(input, p) {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("ShouldFilterLine returned true but no pattern matched in %q", input)
			}
		}
	})
}

func FuzzLimitedBuffer(f *testing.F) {
	f.Add([]byte("hello world"), 5)
	f.Add([]byte("short"), 100)
	f.Add([]byte(""), 0)
	f.Add([]byte("exactly"), 7)
	f.Fuzz(func(t *testing.T, data []byte, max int) {
		if max < 0 {
			return // skip invalid max
		}
		lb := &ioutil.LimitedBuffer{Max: max}
		n, err := lb.Write(data)
		if err != nil {
			t.Fatalf("Write should never error, got: %v", err)
		}
		if n != len(data) {
			t.Fatalf("Write returned %d, want %d", n, len(data))
		}
		// Buffer content must not exceed Max
		if len(lb.String()) > max {
			t.Fatalf("buffer len %d exceeds max %d", len(lb.String()), max)
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
