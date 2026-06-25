package parsing_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/cplieger/fclones-wrapper/internal/parsing"
)

// generateReport builds a synthetic fclones report with n groups, each having
// filesPerGroup paths (1 keeper + filesPerGroup-1 duplicates).
func generateReport(groups, filesPerGroup int) string {
	var b strings.Builder
	b.WriteString("# Redundant: 1000 files (5.6 GB)\n")
	fmt.Fprintf(&b, "# Total: %d %d groups\n\n", groups*filesPerGroup, groups)
	for i := range groups {
		fmt.Fprintf(&b, "abc%08x, 1024 B * %d:\n", i, filesPerGroup)
		for j := range filesPerGroup {
			fmt.Fprintf(&b, "/data/media/group%d/file%d.mkv\n", i, j)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func BenchmarkParseDuplicateGroups(b *testing.B) {
	cases := []struct {
		name          string
		groups        int
		filesPerGroup int
	}{
		{"small_10x2", 10, 2},
		{"medium_100x3", 100, 3},
		{"large_1000x2", 1000, 2},
	}
	for _, tc := range cases {
		report := generateReport(tc.groups, tc.filesPerGroup)
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				parsing.ParseDuplicateGroups(report)
			}
		})
	}
}

func BenchmarkParseStats(b *testing.B) {
	cases := []struct {
		name          string
		groups        int
		filesPerGroup int
	}{
		{"small_10x2", 10, 2},
		{"medium_100x3", 100, 3},
		{"large_1000x2", 1000, 2},
	}
	for _, tc := range cases {
		report := generateReport(tc.groups, tc.filesPerGroup)
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				parsing.ParseStats(report)
			}
		})
	}
}
