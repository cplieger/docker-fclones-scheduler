package parsing_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/cplieger/fclones-wrapper/internal/parsing"
)

// generateJSONReport builds a synthetic fclones JSON report with n groups,
// each having filesPerGroup paths (1 keeper + filesPerGroup-1 duplicates),
// matching the pinned upstream wire shape.
func generateJSONReport(groups, filesPerGroup int) string {
	var b strings.Builder
	b.WriteString(`{"header":{"version":"0.35.0","timestamp":"2026-07-17T00:00:00+00:00","command":["fclones","group"],"base_dir":"/","stats":{`)
	fmt.Fprintf(&b, `"group_count":%d,"total_file_count":%d,"total_file_size":%d,"redundant_file_count":%d,"redundant_file_size":%d,"missing_file_count":0,"missing_file_size":0}},"groups":[`,
		groups, groups*filesPerGroup, groups*filesPerGroup*1024,
		groups*(filesPerGroup-1), groups*(filesPerGroup-1)*1024)
	for i := range groups {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"file_len":1024,"file_hash":"abc%08x","files":[`, i)
		for j := range filesPerGroup {
			if j > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `"/data/media/group%d/file%d.mkv"`, i, j)
		}
		b.WriteString(`]}`)
	}
	b.WriteString(`]}`)
	return b.String()
}

func BenchmarkDecodeReport(b *testing.B) {
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
		report := generateJSONReport(tc.groups, tc.filesPerGroup)
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := parsing.DecodeReport(strings.NewReader(report), 100); err != nil {
					b.Fatalf("DecodeReport: %v", err)
				}
			}
		})
	}
}
