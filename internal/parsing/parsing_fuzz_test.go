package parsing_test

import (
	"math"
	"strings"
	"testing"

	"github.com/cplieger/fclones-wrapper/internal/parsing"
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
		// Size must be non-empty (at least defaultSizeStr)
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
	// Adversarial: duplicate paths that themselves look like group headers
	// (embed ',', '*' and a trailing ':'). Exercises in-group path handling so
	// a header-like filename is not mistaken for the start of a new group.
	f.Add("h, 1 B * 2:\n/x, 1 B * 2:\n/y, 1 B * 2:\n\n")
	// Adversarial: filenames that embed a newline (legal on Unix). fclones
	// writes scanned paths into a newline-delimited report, so an embedded
	// newline can split one path across two parser lines or fake the
	// blank-line group terminator. These seeds lock panic-freedom and the
	// keeper/duplicate structural invariants under newline injection;
	// attribution is best-effort and display-only (see scheduler.go), so no
	// oracle assertion is needed.
	f.Add("h, 1 B * 2:\n/keeper\n/dup-line1\ndup-line2\n")     // one dup path split across two lines
	f.Add("h, 1 B * 2:\n/keeper\n/dup-part\n\ntrailing\n")     // embedded blank line fakes the group terminator
	f.Add("h, 1 B * 2:\n/keeper\n# Total: 0 0 groups\n/dup\n") // embedded '#'-segment masquerades as a comment line
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
			// Every duplicate path must be non-empty.
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
		if summary.Files < 0 {
			t.Fatalf("Files must be >= 0, got %d", summary.Files)
		}
		if summary.ReclaimedBytes < 0 {
			t.Fatalf("ReclaimedBytes must be >= 0, got %d", summary.ReclaimedBytes)
		}
		// Files and ReclaimedBytes are only assigned inside the block that
		// first populates RawLine, so a non-zero metric implies a non-empty RawLine.
		if (summary.Files > 0 || summary.ReclaimedBytes > 0) && summary.RawLine == "" {
			t.Fatalf("Files=%d ReclaimedBytes=%d set but RawLine empty: input=%q",
				summary.Files, summary.ReclaimedBytes, input)
		}
		// Provenance: a non-empty RawLine must be derivable from the input --
		// either the TrimSpace of a whole line (fallback path) or
		// TrimSpace(line[idx:]) at the "Processed" offset (match path). This
		// grounds the parser's output in its input so a fabricated RawLine
		// cannot pass.
		if summary.RawLine != "" {
			derivable := false
			for line := range strings.SplitSeq(input, "\n") {
				if strings.TrimSpace(line) == summary.RawLine {
					derivable = true
					break
				}
				if idx := strings.Index(line, "Processed"); idx != -1 &&
					strings.TrimSpace(line[idx:]) == summary.RawLine {
					derivable = true
					break
				}
			}
			if !derivable {
				t.Fatalf("RawLine %q not derivable from any input line: input=%q",
					summary.RawLine, input)
			}
		}
	})
}

func FuzzParseRedundantSize(f *testing.F) {
	f.Add("# Redundant: 5 files (1.2 GB)")
	f.Add("# Redundant: 512 MB")
	f.Add("# Redundant: 512")
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
	// Guard regression seeds: NaN/Inf/overflow/negative all clamp to 0.
	f.Add("Inf KB")
	f.Add("-Inf KB")
	f.Add("NaN MB")
	f.Add("-5 KB")
	f.Add("99999999 TB")
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
	f.Add(int64(1_000_000_000_000_000))     // 1 PB
	f.Add(int64(1_000_000_000_000_000_000)) // 1 EB
	f.Add(int64(math.MaxInt64))             // exabyte-scale upper bound (regression seed for the EB panic + overflow guard)
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
		// Round-trip must stay within the one-decimal-mantissa
		// precision HumanBytes can represent (relative error < 10%).
		// This is the same bound the rapid property asserts, applied
		// here so the coverage-guided weekly run is meaningful too.
		if n > 0 {
			diff := parsed - n
			if diff < 0 {
				diff = -diff
			}
			if float64(diff) > 0.1*float64(n) {
				t.Fatalf("round-trip exceeded 10%% tolerance: HumanBytes(%d)=%q -> %d", n, s, parsed)
			}
		}
	})
}

func FuzzIsGroupHeader(f *testing.F) {
	f.Add("3a2b,1024 B,2 * 512 B:")
	f.Add("some random line")
	// Adversarial: a duplicate-file path that embeds the header delimiters
	// (',', '*', trailing ':') and is therefore indistinguishable from a real
	// group header by IsGroupHeader alone.
	f.Add("/x, 1 B * 2:")
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
	f.Add("3a2b, 512 B * 2:")
	f.Add("hash, 100 KB, 3 * 100 KB:")
	f.Add("")
	f.Fuzz(func(t *testing.T, input string) {
		result := parsing.ExtractGroupSize(input)
		if result != strings.TrimSpace(result) {
			t.Fatalf("ExtractGroupSize(%q) = %q, not whitespace-trimmed", input, result)
		}
		if len(result) > len(input) {
			t.Fatalf("ExtractGroupSize(%q) = %q longer than input (%d > %d)",
				input, result, len(result), len(input))
		}
	})
}
