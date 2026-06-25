package ioutil

import (
	"strings"
	"testing"
)

// shouldFilterLine is unexported (no production caller outside this package),
// so its tests live here in the internal (package ioutil) test file rather
// than the external ioutil_test package. The external tests cover the public
// surface (FilteringWriter / LimitedBuffer / ReadFileWithLimit); these pin the
// noise-filter predicate directly. FuzzShouldFilterLine deliberately
// re-declares the pattern list as an independent oracle (not filteredPatterns),
// so a wrong edit to either side is caught.

func TestShouldFilterLine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		line string
		drop bool
	}{
		{"[2026-04-26 11:00:03.020] fclones: warn: File system zfs on device data/media doesn't support FIEMAP ioctl API. This is generally harmless.", true},
		{"[2026-04-26 11:00:00.098] fclones:  info: Started grouping", true},
		{"[2026-04-26 11:00:00.098] fclones:  info: Started deduplicating", true},
		{"[2026-04-26 11:00:02.868] fclones:  info: Scanned 238597 file entries", true},
		{"[2026-04-26 11:00:03.019] fclones:  info: Found 1148 (291.3 GB) candidates after grouping by size", true},
		{"[2026-04-26 11:00:03.072] fclones:  info: Processed 6 files and reclaimed 15.1 KB space", false},
		{"[2026-04-26 11:00:03.072] fclones:  warn: cannot read file /scandir/broken: permission denied", false},
		{"[2026-04-26 11:00:03.072] fclones: error: cache corruption detected", false},
		{`time=... level=INFO msg="scan complete" scan_id=abc`, false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			t.Parallel()
			if got := shouldFilterLine(tt.line); got != tt.drop {
				t.Errorf("shouldFilterLine(%q) = %v, want %v", tt.line, got, tt.drop)
			}
		})
	}
}

func FuzzShouldFilterLine(f *testing.F) {
	f.Add("info: Started grouping files")
	f.Add("info: Scanned 100 files")
	f.Add("doesn't support FIEMAP ioctl API")
	f.Add("normal output line")
	f.Add("")
	f.Fuzz(func(t *testing.T, input string) {
		patterns := []string{
			"doesn't support FIEMAP ioctl API",
			"info: Started grouping",
			"info: Started deduplicating",
			"info: Scanned ",
			"info: Found ",
		}
		want := false
		for _, p := range patterns {
			if strings.Contains(input, p) {
				want = true
				break
			}
		}
		if got := shouldFilterLine(input); got != want {
			t.Fatalf("shouldFilterLine(%q) = %v, want %v", input, got, want)
		}
	})
}
