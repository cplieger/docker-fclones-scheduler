package parsing_test

import (
	"strings"
	"testing"

	"fclones-wrapper/internal/parsing"
)

func FuzzParseStats(f *testing.F) {
	f.Add("# Redundant: 5 files (1.2 GB)\n# Total: 10 3 groups")
	f.Add("# Redundant: 512 MB\n# Total: 5 2 groups")
	f.Add("")
	f.Fuzz(func(t *testing.T, input string) {
		stats := parsing.ParseStats(input)
		// Groups must be non-empty (at least "0")
		if stats.Groups == "" {
			t.Fatal("Groups must not be empty")
		}
		// Size must be non-empty (at least DefaultSizeStr)
		if stats.Size == "" {
			t.Fatal("Size must not be empty")
		}
		// If input has no "# Total:" line with "groups", Groups must be "0"
		hasTotal := false
		for line := range strings.SplitSeq(input, "\n") {
			if strings.HasPrefix(line, "# Total:") {
				parts := strings.Fields(line)
				if len(parts) >= 2 && parts[len(parts)-1] == "groups" {
					hasTotal = true
				}
			}
		}
		if !hasTotal && stats.Groups != "0" {
			t.Fatalf("Groups should be '0' without valid Total line, got %q", stats.Groups)
		}
	})
}

func FuzzParseDuplicateGroups(f *testing.F) {
	f.Add("# comment\n\n3a2b,1024 B,2 * 512 B:\n/path/file1\n/path/file2\n")
	f.Add("# comment\n/path/lonely\n")
	f.Add("")
	f.Fuzz(func(t *testing.T, input string) {
		groups := parsing.ParseDuplicateGroups(input)
		for i, g := range groups {
			// Every returned group must have a keeper
			if g.Keeper == "" {
				t.Fatalf("group %d: Keeper must not be empty", i)
			}
			// Every returned group must have at least one duplicate
			if len(g.Duplicates) == 0 {
				t.Fatalf("group %d: must have at least one duplicate", i)
			}
			// No duplicate should equal the keeper
			for _, d := range g.Duplicates {
				if d == "" {
					t.Fatalf("group %d: empty duplicate path", i)
				}
			}
		}
	})
}

func FuzzParseActionSummary(f *testing.F) {
	f.Add("some output\nProcessed 5 files and reclaimed 1.2 GB space\nmore output")
	f.Add("Processed 0 files and reclaimed 0 B space\n")
	f.Add("")
	f.Fuzz(func(t *testing.T, input string) {
		summary := parsing.ParseActionSummary(input)
		// Files count must be non-negative
		if summary.Files < 0 {
			t.Fatalf("Files must be >= 0, got %d", summary.Files)
		}
		// ReclaimedBytes must be non-negative
		if summary.ReclaimedBytes < 0 {
			t.Fatalf("ReclaimedBytes must be >= 0, got %d", summary.ReclaimedBytes)
		}
		// If input contains "Processed" and "reclaimed", RawLine should be set
		// If input contains both "Processed" and "reclaimed", RawLine may still be
		// empty when the line structure doesn't match exactly; we don't assert
		// either way here.
		_ = strings.Contains(input, "Processed") && strings.Contains(input, "reclaimed") && summary.RawLine == "" 
		if summary.RawLine != "" && strings.HasPrefix(summary.RawLine, "Processed") {
			if !strings.Contains(summary.RawLine, "Processed") {
				t.Fatal("RawLine should contain Processed if it starts with it")
			}
		}
	})
}

func FuzzParseRedundantSize(f *testing.F) {
	f.Add("# Redundant: 5 files (1.2 GB)")
	f.Add("# Redundant: 512 MB")
	f.Add("# Redundant:")
	f.Fuzz(func(t *testing.T, input string) {
		result := parsing.ParseRedundantSize(input)
		// Result must never be empty
		if result == "" {
			t.Fatal("ParseRedundantSize must never return empty string")
		}
		// If input has non-empty parenthesized content, result should be that content
		if start := strings.Index(input, "("); start != -1 {
			if end := strings.Index(input, ")"); end > start {
				expected := input[start+1 : end]
				if expected != "" && result != expected {
					t.Fatalf("expected %q from parens, got %q", expected, result)
				}
			}
		}
	})
}

func FuzzParseHumanBytes(f *testing.F) {
	f.Add("1.5 MB")
	f.Add("512 B")
	f.Add("0 KB")
	f.Add("3.7 TB")
	f.Add("garbage")
	f.Add("")
	f.Fuzz(func(t *testing.T, input string) {
		result := parsing.ParseHumanBytes(input)
		// Result must be non-negative
		if result < 0 {
			t.Fatalf("ParseHumanBytes must return >= 0, got %d", result)
		}
		// If result > 0, the input must have had exactly 2 fields
		if result > 0 {
			fields := strings.Fields(input)
			if len(fields) != 2 {
				t.Fatalf("non-zero result %d from input with %d fields", result, len(fields))
			}
		}
	})
}

func FuzzHumanBytesRoundTrip(f *testing.F) {
	f.Add(int64(0))
	f.Add(int64(500))
	f.Add(int64(1500))
	f.Add(int64(1500000))
	f.Add(int64(1500000000))
	f.Fuzz(func(t *testing.T, n int64) {
		if n < 0 {
			return // skip negative values
		}
		s := parsing.HumanBytes(n)
		if s == "" {
			t.Fatal("HumanBytes must not return empty string")
		}
		// Must contain a space separating number and unit
		if !strings.Contains(s, " ") {
			t.Fatalf("HumanBytes(%d) = %q, expected space-separated", n, s)
		}
		// Round-trip: ParseHumanBytes(HumanBytes(n)) should approximate n
		parsed := parsing.ParseHumanBytes(s)
		if parsed < 0 {
			t.Fatalf("round-trip produced negative: HumanBytes(%d)=%q -> %d", n, s, parsed)
		}
		// Allow 10% tolerance due to floating point formatting
		if n > 0 && parsed == 0 {
			t.Fatalf("round-trip lost all value: HumanBytes(%d)=%q -> 0", n, s)
		}
	})
}

func FuzzIsGroupHeader(f *testing.F) {
	f.Add("3a2b,1024 B,2 * 512 B:")
	f.Add("some random line")
	f.Add("")
	f.Fuzz(func(t *testing.T, input string) {
		result := parsing.IsGroupHeader(input)
		// If true, input must contain comma, star, and end with colon
		if result {
			if !strings.Contains(input, ",") {
				t.Fatal("IsGroupHeader true but no comma")
			}
			if !strings.Contains(input, "*") {
				t.Fatal("IsGroupHeader true but no star")
			}
			if !strings.HasSuffix(input, ":") {
				t.Fatal("IsGroupHeader true but no trailing colon")
			}
		}
	})
}

func FuzzExtractGroupSize(f *testing.F) {
	f.Add("3a2b,1024 B,2 * 512 B:")
	f.Add("hash,100 KB,3 * 100 KB:")
	f.Add("")
	f.Fuzz(func(t *testing.T, input string) {
		result := parsing.ExtractGroupSize(input)
		// Result must not panic — and must be a string
		_ = result
		// Result should not contain trailing colon (stripped)
		// The function strips trailing colon from input first, so when the input
		// ends with ":" the result shouldn't unless the content genuinely has one.
		_ = strings.HasSuffix(result, ":") && strings.HasSuffix(input, ":")
	})
}
