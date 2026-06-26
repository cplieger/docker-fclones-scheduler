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
// re-declares the noise pattern lists as an independent level-keyed oracle (not
// the production infoProgressPatterns/warnNoisePatterns vars), so a wrong edit to
// either side is caught.

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
		{"[2026-04-26 11:00:03.072] fclones: warn: cannot read file /scandir/Scanned 5 backups.txt", false},
		{"[2026-04-26 11:00:03.072] fclones: error: failed on /scandir/Found duplicates/x", false},
		// FIEMAP phrase echoed in an attacker-controlled scanned filename inside a
		// genuine error line must NOT be suppressed (h-f1: the FIEMAP marker is a
		// warn-level body marker, so a real error diagnostic that merely names such
		// a file at a different level still reaches the logs).
		{"[2026-04-26 11:00:03.072] fclones: error: cannot read /scandir/doesn't support FIEMAP ioctl API.txt", false},
		// An info progress marker ("Found ") echoed in an error filename body must
		// likewise survive: progress markers are info-level, so they cannot suppress
		// a genuine error line that happens to name such a file.
		{"[2026-04-26 11:00:03.072] fclones: error: cannot read /scandir/Found duplicates.txt", false},
		{"Scanned 100 file entries", false},
		{"[2026-04-26 11:00:03.072] fclones: error: Started grouping cache rebuild failed", false},
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
		// Independent oracle mirroring shouldFilterLine's positional policy:
		// a noise marker is dropped only when it appears in the message body of a
		// genuine "fclones: <level>:" line whose level matches the marker's level
		// (info-progress markers at info, the FIEMAP notice at warn). A marker
		// planted anywhere else (e.g. echoed in a scanned filename inside a
		// warn/error line, or ahead of the fclones prefix) must NOT be filtered.
		noiseByLevel := map[string][]string{
			"info": {"Started grouping", "Started deduplicating", "Scanned ", "Found "},
			"warn": {"doesn't support FIEMAP ioctl API"},
		}
		want := false
		const prefix = "fclones:"
		if _, after, found := strings.Cut(input, prefix); found {
			rest := strings.TrimLeft(after, " ")
			if level, msg, ok := strings.Cut(rest, ":"); ok {
				for _, p := range noiseByLevel[strings.TrimSpace(level)] {
					if strings.Contains(msg, p) {
						want = true
						break
					}
				}
			}
		}
		if got := shouldFilterLine(input); got != want {
			t.Fatalf("shouldFilterLine(%q) = %v, want %v", input, got, want)
		}
	})
}
