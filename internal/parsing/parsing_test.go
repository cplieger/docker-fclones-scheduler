package parsing_test

import (
	"strings"
	"testing"

	"github.com/cplieger/fclones-wrapper/internal/parsing"
	"pgregory.net/rapid"
)

func TestParseStats(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		input       string
		groups      string
		size        string
		totalParsed bool
	}{
		{
			name:        "standard output with parenthesized size",
			input:       "# Redundant: 5 files (1.2 GB)\n# Total: 10 3 groups",
			groups:      "3",
			size:        "1.2 GB",
			totalParsed: true,
		},
		{
			name:        "size without parentheses",
			input:       "# Redundant: 512 MB\n# Total: 5 2 groups",
			groups:      "2",
			size:        "512 MB",
			totalParsed: true,
		},
		{
			name:        "no duplicates",
			input:       "# Redundant: 0 files\n# Total: 0 0 groups",
			groups:      "0",
			size:        "0 files",
			totalParsed: true,
		},
		{
			name:        "real output with commas and extra fields",
			input:       "# Redundant: 1,234 files (5.6 GB) in 789 groups\n# Total: 2,000 789 groups",
			groups:      "789",
			size:        "5.6 GB",
			totalParsed: true,
		},
		{
			name:        "empty input returns defaults",
			input:       "",
			groups:      "0",
			size:        "0 B",
			totalParsed: false,
		},
		{
			name:        "partial output with Total line only",
			input:       "# Total: 5 groups\n",
			groups:      "5",
			size:        "0 B",
			totalParsed: true,
		},
		{
			name:        "Total line without groups suffix",
			input:       "# Total: 100 files\n",
			groups:      "0",
			size:        "0 B",
			totalParsed: false,
		},
		{
			name:        "Redundant only without Total",
			input:       "# Redundant: 3 files (42 MB)\n",
			groups:      "0",
			size:        "42 MB",
			totalParsed: false,
		},
		{
			name:        "Redundant without Total larger size",
			input:       "# Redundant: 10 files (2.5 GB)\n",
			groups:      "0",
			size:        "2.5 GB",
			totalParsed: false,
		},
		{
			name:        "Total with files suffix not groups",
			input:       "# Total: 100 files\n",
			groups:      "0",
			size:        "0 B",
			totalParsed: false,
		},
		{
			name:        "multiple Redundant lines last wins",
			input:       "# Redundant: 1 files (100 MB)\n# Redundant: 2 files (200 MB)\n",
			groups:      "0",
			size:        "200 MB",
			totalParsed: false,
		},
		{
			name:        "Total line with no count fields returns defaults",
			input:       "# Total:",
			groups:      "0",
			size:        "0 B",
			totalParsed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stats := parsing.ParseStats(tt.input)
			if stats.Groups != tt.groups {
				t.Errorf("Groups = %q, want %q", stats.Groups, tt.groups)
			}
			if stats.Size != tt.size {
				t.Errorf("Size = %q, want %q", stats.Size, tt.size)
			}
			if stats.TotalParsed != tt.totalParsed {
				t.Errorf("TotalParsed = %v, want %v", stats.TotalParsed, tt.totalParsed)
			}
		})
	}
}

func TestParseDuplicateGroups(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  []parsing.DuplicateGroup
	}{
		{
			name:  "comments only, no groups",
			input: "# some comment\n",
			want:  nil,
		},
		{
			name:  "group header with two files",
			input: "# comment\n\n3a2b, 1024 B (1.0 KB) * 2:\n/path/file1\n/path/file2\n",
			want: []parsing.DuplicateGroup{
				{
					Keeper:     "/path/file1",
					Duplicates: []string{"/path/file2"},
					SizePerDup: "1024 B (1.0 KB)",
				},
			},
		},
		{
			name: "multiple groups with headers",
			input: "# comment\n\nabc, 100 B * 2:\n/group1/a\n/group1/b\n\n" +
				"def, 200 KB (200.0 KB) * 3:\n/group2/a\n/group2/b\n/group2/c\n",
			want: []parsing.DuplicateGroup{
				{
					Keeper:     "/group1/a",
					Duplicates: []string{"/group1/b"},
					SizePerDup: "100 B",
				},
				{
					Keeper:     "/group2/a",
					Duplicates: []string{"/group2/b", "/group2/c"},
					SizePerDup: "200 KB (200.0 KB)",
				},
			},
		},
		{
			name:  "files without a group header are ignored",
			input: "# comment\n/path/lonely\n",
			want:  nil,
		},
		{
			name:  "empty input",
			input: "",
			want:  nil,
		},
		{
			name:  "only blank lines",
			input: "\n\n\n",
			want:  nil,
		},
		{
			name:  "header but no files",
			input: "abc, 100 B * 2:\n\n",
			want:  nil,
		},
		{
			name:  "header with single file no duplicates",
			input: "abc, 100 B * 2:\n/path/only\n",
			want:  nil,
		},
		{
			name:  "whitespace in paths is trimmed",
			input: "abc, 100 B * 2:\n  /path/with spaces  \n  /path/another  \n",
			want: []parsing.DuplicateGroup{
				{
					Keeper:     "/path/with spaces",
					Duplicates: []string{"/path/another"},
					SizePerDup: "100 B",
				},
			},
		},
		{
			name:  "multiple headers sequential",
			input: "abc, 100 B * 2:\n/file1\n/file2\n\nxyz, 200 B * 2:\n/file3\n/file4\n",
			want: []parsing.DuplicateGroup{
				{
					Keeper:     "/file1",
					Duplicates: []string{"/file2"},
					SizePerDup: "100 B",
				},
				{
					Keeper:     "/file3",
					Duplicates: []string{"/file4"},
					SizePerDup: "200 B",
				},
			},
		},
		{
			name:  "large group with many files",
			input: "abc, 10 B * 5:\n/f1\n/f2\n/f3\n/f4\n/f5\n",
			want: []parsing.DuplicateGroup{
				{
					Keeper:     "/f1",
					Duplicates: []string{"/f2", "/f3", "/f4", "/f5"},
					SizePerDup: "10 B",
				},
			},
		},
		{
			// Oracle the fuzz invariants miss: duplicate filenames that embed
			// the header delimiters (',', '*', trailing ':') must NOT be
			// reclassified as new group headers. The whole block stays ONE
			// group with the expected keeper and duplicate count.
			name:  "duplicate filenames that look like group headers stay one group",
			input: "realhash, 100 B * 3:\n/path/a, 1 B * 2:\n/path/b, 1 B * 2:\n/path/c, 1 B * 2:\n",
			want: []parsing.DuplicateGroup{
				{
					Keeper:     "/path/a, 1 B * 2:",
					Duplicates: []string{"/path/b, 1 B * 2:", "/path/c, 1 B * 2:"},
					SizePerDup: "100 B",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := parsing.ParseDuplicateGroups(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("groups len = %d, want %d (got %+v)", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i].Keeper != tt.want[i].Keeper {
					t.Errorf("group %d Keeper = %q, want %q", i, got[i].Keeper, tt.want[i].Keeper)
				}
				if got[i].SizePerDup != tt.want[i].SizePerDup {
					t.Errorf("group %d SizePerDup = %q, want %q", i, got[i].SizePerDup, tt.want[i].SizePerDup)
				}
				if len(got[i].Duplicates) != len(tt.want[i].Duplicates) {
					t.Fatalf("group %d Duplicates len = %d, want %d", i, len(got[i].Duplicates), len(tt.want[i].Duplicates))
				}
				for j := range got[i].Duplicates {
					if got[i].Duplicates[j] != tt.want[i].Duplicates[j] {
						t.Errorf("group %d Duplicates[%d] = %q, want %q",
							i, j, got[i].Duplicates[j], tt.want[i].Duplicates[j])
					}
				}
			}
		})
	}
}

func TestParseActionSummary(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		input          string
		wantRawContain string
		wantFiles      int
		wantReclaimed  int64
	}{
		{
			name:           "standard Processed line",
			input:          "some output\nProcessed 5 files and reclaimed 1.2 GB space\nmore output",
			wantFiles:      5,
			wantReclaimed:  1_200_000_000,
			wantRawContain: "Processed 5 files",
		},
		{
			name:           "zero files zero bytes",
			input:          "Processed 0 files and reclaimed 0 B space\n",
			wantFiles:      0,
			wantReclaimed:  0,
			wantRawContain: "Processed 0 files",
		},
		{
			name:           "falls back to last non-empty line",
			input:          "some output\nlast line",
			wantFiles:      0,
			wantReclaimed:  0,
			wantRawContain: "last line",
		},
		{
			name:           "empty input",
			input:          "",
			wantFiles:      0,
			wantReclaimed:  0,
			wantRawContain: "",
		},
		{
			name:           "Processed line with 512 KB",
			input:          "Processed 2 files and reclaimed 512 KB space\n",
			wantFiles:      2,
			wantReclaimed:  512_000,
			wantRawContain: "Processed 2 files",
		},
		{
			name:           "only whitespace",
			input:          "   \n  \n  ",
			wantFiles:      0,
			wantReclaimed:  0,
			wantRawContain: "",
		},
		{
			name:           "Processed without reclaimed falls back",
			input:          "Processed 5 files\nlast line",
			wantFiles:      0,
			wantReclaimed:  0,
			wantRawContain: "last line",
		},
		{
			name:           "Processed mid-line with prefix",
			input:          "INFO: Processed 10 files and reclaimed 500 MB space",
			wantFiles:      10,
			wantReclaimed:  500_000_000,
			wantRawContain: "Processed",
		},
		{
			name:           "first Processed match wins",
			input:          "Processed 5 files and reclaimed 1 GB space\nProcessed 10 files and reclaimed 2 GB space\n",
			wantFiles:      5,
			wantReclaimed:  1_000_000_000,
			wantRawContain: "Processed 5 files",
		},
		{
			name:           "reclaimed without Processed falls back",
			input:          "reclaimed 500 MB\nlast line",
			wantFiles:      0,
			wantReclaimed:  0,
			wantRawContain: "last line",
		},
		{
			name:           "zero files with non-zero reclaim parses both independently",
			input:          "Processed 0 files and reclaimed 4 B space",
			wantFiles:      0,
			wantReclaimed:  4,
			wantRawContain: "Processed 0 files",
		},
		{
			name:           "negative file count is rejected, reclaim still parsed",
			input:          "Processed -3 files and reclaimed 4 B space",
			wantFiles:      0,
			wantReclaimed:  4,
			wantRawContain: "Processed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := parsing.ParseActionSummary(tt.input)
			if got.Files != tt.wantFiles {
				t.Errorf("Files = %d, want %d", got.Files, tt.wantFiles)
			}
			if got.ReclaimedBytes != tt.wantReclaimed {
				t.Errorf("ReclaimedBytes = %d, want %d", got.ReclaimedBytes, tt.wantReclaimed)
			}
			if tt.wantRawContain != "" && !strings.Contains(got.RawLine, tt.wantRawContain) {
				t.Errorf("RawLine = %q, want to contain %q", got.RawLine, tt.wantRawContain)
			}
			if tt.wantRawContain == "" && tt.input != "" && got.RawLine != "" {
				// For whitespace-only input, RawLine should be empty
				if strings.TrimSpace(tt.input) == "" && got.RawLine != "" {
					t.Errorf("RawLine = %q, want empty for whitespace-only input", got.RawLine)
				}
			}
		})
	}
}

func TestParseRedundantSize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		line string
		want string
	}{
		{"parenthesized", "# Redundant: 5 files (1.2 GB)", "1.2 GB"},
		{"bare size", "# Redundant: 512 MB", "512 MB"},
		{"too few fields", "# Redundant:", "0 B"},
		{"unmatched paren", "# Redundant: 5 files (1.2 GB", "5 files"},
		{"empty parens", "# Redundant: 5 files ()", "5 files"},
		{"nested parens", "# Redundant: 5 files (1.2 GB (approx))", "1.2 GB (approx"},
		{"close paren before open paren", "a ) (", "0 B"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := parsing.ParseRedundantSize(tt.line)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractGroupSize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		header string
		want   string
	}{
		{"677a3ec86f3207b994f1ec816f4b3dcc, 5000 B (5.0 KB) * 3:", "5000 B (5.0 KB)"},
		{"abc, 100 B * 2:", "100 B"},
		{"def, 200 KB (200.0 KB) * 3:", "200 KB (200.0 KB)"},
		{"abc,100 B,2 * 50 B:", "100 B,2"},
		{"abc, 100 B * 2", "100 B"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.header, func(t *testing.T) {
			t.Parallel()
			if got := parsing.ExtractGroupSize(tt.header); got != tt.want {
				t.Errorf("ExtractGroupSize(%q) = %q, want %q", tt.header, got, tt.want)
			}
		})
	}
}

func TestHumanBytes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		want string
		in   int64
	}{
		{in: 0, want: "0 B"},
		{in: 999, want: "999 B"},
		{in: 1000, want: "1.0 KB"},
		{in: 1500, want: "1.5 KB"},
		{in: 15100, want: "15.1 KB"},
		{in: 1_000_000, want: "1.0 MB"},
		{in: 1_234_567, want: "1.2 MB"},
		{in: 1_000_000_000, want: "1.0 GB"},
		{in: 591_700_000_000_000, want: "591.7 TB"},
		{in: 1_000_000_000_000_000, want: "1.0 PB"},
		{in: 1_500_000_000_000_000, want: "1.5 PB"},
		{in: 1_000_000_000_000_000_000, want: "1.0 EB"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			if got := parsing.HumanBytes(tt.in); got != tt.want {
				t.Errorf("HumanBytes(%d) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// --- Property-based tests ---

func TestProperty_ParseStatsAlwaysReturnsDefaults(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		input := rapid.String().Draw(rt, "input")
		stats := parsing.ParseStats(input)
		if stats.Groups == "" {
			rt.Fatalf("ParseStats(%q).Groups is empty", input)
		}
		if stats.Size == "" {
			rt.Fatalf("ParseStats(%q).Size is empty", input)
		}
	})
}

func TestProperty_ParseStatsIsDeterministic(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		input := rapid.String().Draw(rt, "input")
		a := parsing.ParseStats(input)
		b := parsing.ParseStats(input)
		if a.Groups != b.Groups {
			rt.Fatalf("ParseStats(%q) non-deterministic: Groups %q vs %q", input, a.Groups, b.Groups)
		}
		if a.Size != b.Size {
			rt.Fatalf("ParseStats(%q) non-deterministic: Size %q vs %q", input, a.Size, b.Size)
		}
	})
}

func TestProperty_ParseDuplicateGroupsNeverPanics(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		input := rapid.String().Draw(rt, "input")
		got := parsing.ParseDuplicateGroups(input)
		for i, g := range got {
			if g.Keeper == "" {
				rt.Fatalf("group %d has empty Keeper: input=%q", i, input)
			}
			if len(g.Duplicates) == 0 {
				rt.Fatalf("group %d has no Duplicates: input=%q", i, input)
			}
			for j, d := range g.Duplicates {
				if d == "" {
					rt.Fatalf("group %d Duplicates[%d] is empty: input=%q", i, j, input)
				}
			}
		}
	})
}

func TestProperty_ParseActionSummaryNeverNegative(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		input := rapid.String().Draw(rt, "input")
		got := parsing.ParseActionSummary(input)
		if got.Files < 0 {
			rt.Fatalf("ParseActionSummary(%q).Files = %d, want >= 0", input, got.Files)
		}
		if got.ReclaimedBytes < 0 {
			rt.Fatalf("ParseActionSummary(%q).ReclaimedBytes = %d, want >= 0",
				input, got.ReclaimedBytes)
		}
	})
}

func TestProperty_ParseActionSummaryIsDeterministic(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		input := rapid.String().Draw(rt, "input")
		a := parsing.ParseActionSummary(input)
		b := parsing.ParseActionSummary(input)
		if a.Files != b.Files || a.ReclaimedBytes != b.ReclaimedBytes ||
			a.RawLine != b.RawLine {
			rt.Fatalf("ParseActionSummary(%q) non-deterministic: %+v vs %+v", input, a, b)
		}
	})
}

func TestProperty_ParseRedundantSizeNeverPanics(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		input := rapid.String().Draw(rt, "input")
		parsing.ParseRedundantSize(input)
	})
}

func TestProperty_ParseRedundantSizeAlwaysReturns(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		suffix := rapid.String().Draw(rt, "suffix")
		line := "# Redundant: " + suffix
		parsing.ParseRedundantSize(line)
	})
}

// Property: buildScanArgs-like structure test for ParseStats.
func TestProperty_BuildScanArgsStructure(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		scanPath := rapid.StringMatching(`/[a-z]{1,10}(/[a-z]{1,10}){0,3}`).Draw(rt, "scanPath")
		numArgs := rapid.IntRange(0, 3).Draw(rt, "numArgs")
		argTokens := make([]string, numArgs*2)
		for i := 0; i < numArgs*2; i += 2 {
			argTokens[i] = rapid.StringMatching(`--[a-z\-]{1,10}`).Draw(rt, "flag")
			argTokens[i+1] = rapid.StringMatching(`[a-zA-Z0-9]{1,10}`).Draw(rt, "value")
		}
		// This property test validates that ParseStats doesn't panic on structured input
		input := "# Redundant: " + scanPath + "\n# Total: " + strings.Join(argTokens, " ") + " groups\n"
		stats := parsing.ParseStats(input)
		if stats.Groups == "" {
			rt.Fatalf("Groups is empty for structured input")
		}
	})
}

// TestParseRedundantSize_parenAtIndexOne guards the "(" found/not-found boundary
// in ParseRedundantSize. The lookup is `start := strings.Index(line, "(")` gated
// by `start != -1`. Mutating that sentinel to `start != 1` (INVERT_NEGATIVES /
// ARITHMETIC_BASE on the -1 literal) makes the function skip the parenthesised
// branch precisely when "(" sits at index 1, falling back to the bare-fields
// path. A "(" at any other index (as in every other test case) hides the
// mutation, so this case pins the sentinel value itself.
func TestParseRedundantSize_parenAtIndexOne(t *testing.T) {
	t.Parallel()

	// given a line whose "(" is at index 1 and a "512 MB" payload inside it
	line := "x(512 MB)"

	// when the redundant size is parsed
	got := parsing.ParseRedundantSize(line)

	// then the parenthesised payload wins (real code), not the "0 B" fallback
	// that `start != 1` would force
	want := "512 MB"
	if got != want {
		t.Errorf("ParseRedundantSize(%q) = %q, want %q", line, got, want)
	}
}

// TestExtractGroupSize_commaAtIndexOne guards the comma found/not-found boundary
// in ExtractGroupSize. After trimming the trailing ":" and the " * <per-dup>"
// suffix, the remaining header is split at its first comma via
// `idx := strings.Index(h, ",")` gated by `idx != -1`. Mutating that sentinel to
// `idx != 1` (INVERT_NEGATIVES / ARITHMETIC_BASE on the -1 literal) skips the
// hash-stripping step exactly when the comma is at index 1, i.e. when the hash
// is a single character. Every other test uses a multi-character hash, so the
// comma never lands at index 1 and the mutation survives.
func TestExtractGroupSize_commaAtIndexOne(t *testing.T) {
	t.Parallel()

	// given a group header with a single-character hash "a" (comma at index 1)
	header := "a,100 B,2 * 50 B:"

	// when the per-duplicate size is extracted
	got := parsing.ExtractGroupSize(header)

	// then the leading hash is stripped (real code); `idx != 1` would leave it,
	// yielding "a,100 B,2"
	want := "100 B,2"
	if got != want {
		t.Errorf("ExtractGroupSize(%q) = %q, want %q", header, got, want)
	}
}

// TestParseActionSummary_exactlySevenFields guards the `len(fields) >= 7`
// boundary in ParseActionSummary. The metric-extraction block reads fields[1]
// (file count) and fields[5..6] (reclaimed size), so it requires at least seven
// whitespace-separated tokens. Mutating `>= 7` to `> 7` (CONDITIONALS_BOUNDARY)
// drops the metrics for a line with exactly seven fields. Every other test uses
// the eight-field "...reclaimed X Y space" form, so only this trailing-"space"-
// free line exercises the boundary.
func TestParseActionSummary_exactlySevenFields(t *testing.T) {
	t.Parallel()

	// given a "Processed" line with exactly seven fields (no trailing "space")
	stdout := "Processed 5 files and reclaimed 512 B\n"

	// when the action summary is parsed
	got := parsing.ParseActionSummary(stdout)

	// then both metrics are extracted (real code); `> 7` would leave them at 0
	if got.Files != 5 {
		t.Errorf("ParseActionSummary(%q).Files = %d, want 5", stdout, got.Files)
	}
	if got.ReclaimedBytes != 512 {
		t.Errorf("ParseActionSummary(%q).ReclaimedBytes = %d, want 512", stdout, got.ReclaimedBytes)
	}
}

// TestParseActionSummary_partialParse pins the partial-parse contract.
func TestParseActionSummary_partialParse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, input, wantRaw string
		wantFiles            int
		wantReclaimed        int64
	}{
		{
			"unknown size unit yields files but zero reclaimed",
			"Processed 5 files and reclaimed 1.5 ZB space",
			"Processed 5 files and reclaimed 1.5 ZB space",
			5, 0,
		},
		{
			"non-numeric size yields files but zero reclaimed",
			"Processed 7 files and reclaimed lots of space",
			"Processed 7 files and reclaimed lots of space",
			7, 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := parsing.ParseActionSummary(tt.input)
			if got.Files != tt.wantFiles {
				t.Errorf("Files = %d, want %d", got.Files, tt.wantFiles)
			}
			if got.ReclaimedBytes != tt.wantReclaimed {
				t.Errorf("ReclaimedBytes = %d, want %d", got.ReclaimedBytes, tt.wantReclaimed)
			}
			if got.RawLine != tt.wantRaw {
				t.Errorf("RawLine = %q, want %q", got.RawLine, tt.wantRaw)
			}
		})
	}
}

// TestParseActionSummary_nonNumericFileCount completes the partial-parse
// contract: the mirror of the existing "non-numeric size" case. When the
// file-count token is not a number the strconv.Atoi error path leaves Files at
// 0 while the well-formed reclaimed size is still parsed independently.
func TestParseActionSummary_nonNumericFileCount(t *testing.T) {
	t.Parallel()

	const stdout = "Processed many files and reclaimed 4 B space"
	got := parsing.ParseActionSummary(stdout)

	if got.Files != 0 {
		t.Errorf("ParseActionSummary(%q).Files = %d, want 0", stdout, got.Files)
	}
	if got.ReclaimedBytes != 4 {
		t.Errorf("ParseActionSummary(%q).ReclaimedBytes = %d, want 4", stdout, got.ReclaimedBytes)
	}
	if got.RawLine != stdout {
		t.Errorf("ParseActionSummary(%q).RawLine = %q, want %q", stdout, got.RawLine, stdout)
	}
}

func TestParseHumanBytes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want int64
	}{
		{"bytes", "512 B", 512},
		{"kilobytes", "1 KB", 1_000},
		{"megabytes float", "1.5 MB", 1_500_000},
		{"gigabytes", "1 GB", 1_000_000_000},
		{"terabytes float", "1.5 TB", 1_500_000_000_000},
		{"zero kilobytes", "0 KB", 0},
		{"single-letter K alias", "5 K", 5_000},
		{"single-letter M alias", "5 M", 5_000_000},
		{"single-letter G alias", "2 G", 2_000_000_000},
		{"single-letter T alias", "3 T", 3_000_000_000_000},
		{"lowercase unit is uppercased", "5 kb", 5_000},
		{"invalid unit returns zero", "100 XB", 0},
		{"non-numeric value returns zero", "abc MB", 0},
		{"single field returns zero", "100", 0},
		{"empty returns zero", "", 0},
		{"overflow rejected (out of int64 range)", "99999999 TB", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := parsing.ParseHumanBytes(tt.in); got != tt.want {
				t.Errorf("ParseHumanBytes(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseHumanBytes_IECUnits(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want int64
	}{
		{"kibibytes", "1 KiB", 1024},
		{"mebibytes", "1 MiB", 1_048_576},
		{"mebibytes float", "1.5 MiB", 1_572_864},
		{"gibibytes", "1 GiB", 1_073_741_824},
		{"tebibytes", "1 TiB", 1_099_511_627_776},
		{"pebibytes", "1 PiB", 1_125_899_906_842_624},
		{"exbibytes", "1 EiB", 1_152_921_504_606_846_976},
		{"lowercase iec is uppercased", "1 gib", 1_073_741_824},
		{"petabytes decimal", "1 PB", 1_000_000_000_000_000},
		{"single-letter P alias", "2 P", 2_000_000_000_000_000},
		{"exabytes decimal", "1 EB", 1_000_000_000_000_000_000},
		{"single-letter E alias", "2 E", 2_000_000_000_000_000_000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := parsing.ParseHumanBytes(tt.in); got != tt.want {
				t.Errorf("ParseHumanBytes(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseHumanBytes_rejectsNonFiniteAndNegative(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want int64
	}{
		{"negative integer rejected", "-5 KB", 0},
		{"negative float rejected", "-1.5 MB", 0},
		{"positive infinity rejected", "Inf KB", 0},
		{"negative infinity rejected", "-Inf KB", 0},
		{"NaN rejected", "NaN MB", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := parsing.ParseHumanBytes(tt.in); got != tt.want {
				t.Errorf("ParseHumanBytes(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestIsGroupHeader(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		line string
		want bool
	}{
		{"valid header with comma star and trailing colon", "3a2b, 512 B * 2:", true},
		{"missing comma", "3a2b 512 B * 2:", false},
		{"missing star", "3a2b, 512 B 2:", false},
		{"missing trailing colon", "3a2b, 512 B * 2", false},
		{"empty string", "", false},
		{"duplicate path embedding all delimiters is still a header", "/x, 1 B * 2:", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := parsing.IsGroupHeader(tt.line); got != tt.want {
				t.Errorf("IsGroupHeader(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

func TestParseDuplicateGroups_commentPrefixedPathLinesAreSkipped(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  []parsing.DuplicateGroup
	}{
		{
			name:  "hash-prefixed line after keeper is skipped and group survives",
			input: "h, 100 B * 2:\n/keeper\n#hashline\n/dup\n",
			want: []parsing.DuplicateGroup{
				{Keeper: "/keeper", Duplicates: []string{"/dup"}, SizePerDup: "100 B"},
			},
		},
		{
			name:  "hash-prefixed duplicate path is silently omitted",
			input: "h, 100 B * 2:\n/keeper\n/dup1\n#dup2\n/dup3\n",
			want: []parsing.DuplicateGroup{
				{Keeper: "/keeper", Duplicates: []string{"/dup1", "/dup3"}, SizePerDup: "100 B"},
			},
		},
		{
			name:  "hash-prefixed keeper line collapses the group to nothing",
			input: "h, 100 B * 2:\n#keeperline\n/realdup\n",
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := parsing.ParseDuplicateGroups(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("ParseDuplicateGroups(%q) len = %d, want %d (got %+v)", tt.input, len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i].Keeper != tt.want[i].Keeper {
					t.Errorf("group %d Keeper = %q, want %q", i, got[i].Keeper, tt.want[i].Keeper)
				}
				if got[i].SizePerDup != tt.want[i].SizePerDup {
					t.Errorf("group %d SizePerDup = %q, want %q", i, got[i].SizePerDup, tt.want[i].SizePerDup)
				}
				if len(got[i].Duplicates) != len(tt.want[i].Duplicates) {
					t.Fatalf("group %d Duplicates len = %d, want %d", i, len(got[i].Duplicates), len(tt.want[i].Duplicates))
				}
				for j := range got[i].Duplicates {
					if got[i].Duplicates[j] != tt.want[i].Duplicates[j] {
						t.Errorf("group %d Duplicates[%d] = %q, want %q", i, j, got[i].Duplicates[j], tt.want[i].Duplicates[j])
					}
				}
			}
		})
	}
}

// TestProperty_HumanBytesParseHumanBytesRoundTrip verifies that ParseHumanBytes
// recovers a value formatted by HumanBytes within the one-decimal-mantissa
// precision HumanBytes can represent (relative error stays well under 10%). It
// is the value-preserving round-trip that FuzzHumanBytesRoundTrip only gestures
// at with a non-zero check.
func TestProperty_HumanBytesParseHumanBytesRoundTrip(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.Int64Range(0, 9223372036854775807).Draw(rt, "n")
		parsed := parsing.ParseHumanBytes(parsing.HumanBytes(n))
		diff := parsed - n
		if diff < 0 {
			diff = -diff
		}
		if float64(diff) > 0.1*float64(n) {
			rt.Fatalf("HumanBytes(%d) round-trips to %d, exceeding 10%% tolerance", n, parsed)
		}
	})
}

// TestParseHumanBytes_overflowBoundary pins floatToInt64's out-of-range guard at
// the exact boundary. float64(math.MaxInt64) rounds up to 2^63
// (9223372036854775808) and ParseFloat reproduces that power of two exactly, so
// "9223372036854775808 B" drives the converted value to precisely the guard
// value, which must clamp to 0. The largest float64 strictly below 2^63
// (9223372036854774784) must pass through unchanged.
func TestParseHumanBytes_overflowBoundary(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want int64
	}{
		{"exactly 2^63 bytes clamps to zero", "9223372036854775808 B", 0},
		{"MaxInt64 string rounds up to 2^63 and clamps", "9223372036854775807 B", 0},
		{"largest float64 below 2^63 passes through", "9223372036854774784 B", 9223372036854774784},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := parsing.ParseHumanBytes(tt.in); got != tt.want {
				t.Errorf("ParseHumanBytes(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

// TestParseActionSummary_shortReclaimLineNoPanic pins the
// `len(fields) < 7` early return in reclaimedMetrics, reached via
// ParseActionSummary. A line containing both "Processed" and
// "reclaimed" but with fewer than seven fields enters the
// metric-extraction block, so reclaimedMetrics is invoked and must
// return zero metrics WITHOUT indexing fields[5]/fields[6] out of
// range. Removing or widening the guard panics on this input.
func TestParseActionSummary_shortReclaimLineNoPanic(t *testing.T) {
	t.Parallel()

	// given a line with both keywords but only four fields
	const stdout = "Processed 5 reclaimed something"

	// when the action summary is parsed
	got := parsing.ParseActionSummary(stdout)

	// then the short-field guard yields zero metrics (no panic)
	// and the matched line is still recorded verbatim
	if got.Files != 0 {
		t.Errorf("ParseActionSummary(%q).Files = %d, want 0", stdout, got.Files)
	}
	if got.ReclaimedBytes != 0 {
		t.Errorf("ParseActionSummary(%q).ReclaimedBytes = %d, want 0", stdout, got.ReclaimedBytes)
	}
	if got.RawLine != stdout {
		t.Errorf("ParseActionSummary(%q).RawLine = %q, want %q", stdout, got.RawLine, stdout)
	}
}
