package parsing_test

import (
	"testing"

	"fclones-wrapper/internal/parsing"
)

func FuzzParseStats(f *testing.F) {
	f.Add("# Redundant: 5 files (1.2 GB)\n# Total: 10 3 groups")
	f.Add("# Redundant: 512 MB\n# Total: 5 2 groups")
	f.Add("")
	f.Fuzz(func(t *testing.T, input string) {
		parsing.ParseStats(input)
	})
}

func FuzzParseDuplicateGroups(f *testing.F) {
	f.Add("# comment\n\n3a2b, 1024 B (1.0 KB) * 2:\n/path/file1\n/path/file2\n")
	f.Add("# comment\n/path/lonely\n")
	f.Add("")
	f.Fuzz(func(t *testing.T, input string) {
		parsing.ParseDuplicateGroups(input)
	})
}

func FuzzParseActionSummary(f *testing.F) {
	f.Add("some output\nProcessed 5 files and reclaimed 1.2 GB space\nmore output")
	f.Add("Processed 0 files and reclaimed 0 B space\n")
	f.Add("")
	f.Fuzz(func(t *testing.T, input string) {
		parsing.ParseActionSummary(input)
	})
}

func FuzzParseRedundantSize(f *testing.F) {
	f.Add("# Redundant: 5 files (1.2 GB)")
	f.Add("# Redundant: 512 MB")
	f.Add("# Redundant:")
	f.Fuzz(func(t *testing.T, input string) {
		parsing.ParseRedundantSize(input)
	})
}
