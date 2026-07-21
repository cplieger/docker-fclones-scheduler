package parsing_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cplieger/docker-fclones-scheduler/internal/parsing"
	"pgregory.net/rapid"
)

// minimalHeader is a valid report header with the given stats JSON (pass
// "null" or omit fields to build error cases).
func reportDoc(statsJSON, groupsJSON string) string {
	return `{"header":{"version":"0.35.0","timestamp":"2026-07-17T00:00:00+00:00",` +
		`"command":["fclones","group","/scandir","-f","json"],"base_dir":"/","stats":` +
		statsJSON + `},"groups":` + groupsJSON + `}`
}

// statsJSON renders a stats block with the given group/redundant counts.
func statsJSON(groupCount, redundantCount int, redundantSize int64) string {
	var b strings.Builder
	b.WriteString(`{"group_count":`)
	b.WriteString(jsonInt(groupCount))
	b.WriteString(`,"total_file_count":10,"total_file_size":99999,"redundant_file_count":`)
	b.WriteString(jsonInt(redundantCount))
	b.WriteString(`,"redundant_file_size":`)
	b.WriteString(jsonInt64(redundantSize))
	b.WriteString(`,"missing_file_count":0,"missing_file_size":0}`)
	return b.String()
}

func jsonInt(n int) string     { b, _ := json.Marshal(n); return string(b) }
func jsonInt64(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestDecodeReport(t *testing.T) {
	t.Parallel()

	twoGroups := `[
		{"file_len":6274,"file_hash":"49165422","files":["/keep/a","/dup/a1","/dup/a2"]},
		{"file_len":41,"file_hash":"dcf2e111","files":["/keep/b","/dup/b1"]}
	]`

	t.Run("happy path maps stats, keeper, and sizes", func(t *testing.T) {
		t.Parallel()
		got, err := parsing.DecodeReport(strings.NewReader(reportDoc(statsJSON(2, 3, 6315), twoGroups)), 100)
		if err != nil {
			t.Fatalf("DecodeReport: %v", err)
		}
		if got.Stats.GroupCount != 2 || got.Stats.RedundantFileCount != 3 || got.Stats.RedundantFileSize != 6315 {
			t.Errorf("Stats = %+v, want group_count=2 redundant_file_count=3 redundant_file_size=6315", got.Stats)
		}
		if got.TotalGroups != 2 || got.TotalDuplicates != 3 {
			t.Errorf("totals = %d groups / %d duplicates, want 2 / 3", got.TotalGroups, got.TotalDuplicates)
		}
		if len(got.Groups) != 2 {
			t.Fatalf("retained groups = %d, want 2", len(got.Groups))
		}
		first := got.Groups[0]
		if first.Keeper != "/keep/a" {
			t.Errorf("group 0 Keeper = %q, want %q (files[0] keep-first)", first.Keeper, "/keep/a")
		}
		if len(first.Duplicates) != 2 || first.Duplicates[0] != "/dup/a1" || first.Duplicates[1] != "/dup/a2" {
			t.Errorf("group 0 Duplicates = %v, want [/dup/a1 /dup/a2]", first.Duplicates)
		}
		if first.SizePerDup != parsing.HumanBytes(6274) {
			t.Errorf("group 0 SizePerDup = %q, want %q", first.SizePerDup, parsing.HumanBytes(6274))
		}
		if got.Groups[1].SizePerDup != "41 B" {
			t.Errorf("group 1 SizePerDup = %q, want %q", got.Groups[1].SizePerDup, "41 B")
		}
	})

	t.Run("empty groups with zero count is a genuine zero", func(t *testing.T) {
		t.Parallel()
		got, err := parsing.DecodeReport(strings.NewReader(reportDoc(statsJSON(0, 0, 0), `[]`)), 100)
		if err != nil {
			t.Fatalf("DecodeReport: %v", err)
		}
		if got.TotalGroups != 0 || got.TotalDuplicates != 0 || len(got.Groups) != 0 {
			t.Errorf("got %+v, want empty report", got)
		}
	})

	t.Run("keepGroups caps retention while totals cover the document", func(t *testing.T) {
		t.Parallel()
		three := `[
			{"file_len":10,"file_hash":"h1","files":["/k1","/d1"]},
			{"file_len":20,"file_hash":"h2","files":["/k2","/d2","/d3"]},
			{"file_len":30,"file_hash":"h3","files":["/k3","/d4"]}
		]`
		got, err := parsing.DecodeReport(strings.NewReader(reportDoc(statsJSON(3, 4, 80), three)), 1)
		if err != nil {
			t.Fatalf("DecodeReport: %v", err)
		}
		if len(got.Groups) != 1 || got.Groups[0].Keeper != "/k1" {
			t.Errorf("retained = %+v, want only the first group", got.Groups)
		}
		if got.TotalGroups != 3 || got.TotalDuplicates != 4 {
			t.Errorf("totals = %d/%d, want 3/4 despite the retention cap", got.TotalGroups, got.TotalDuplicates)
		}
	})

	t.Run("non-positive keepGroups retains nothing", func(t *testing.T) {
		t.Parallel()
		for _, keep := range []int{0, -1} {
			got, err := parsing.DecodeReport(strings.NewReader(reportDoc(statsJSON(2, 3, 6315), twoGroups)), keep)
			if err != nil {
				t.Fatalf("DecodeReport(keep=%d): %v", keep, err)
			}
			if len(got.Groups) != 0 || got.TotalGroups != 2 || got.TotalDuplicates != 3 {
				t.Errorf("keep=%d: got %+v, want no retained groups with full totals", keep, got)
			}
		}
	})

	t.Run("unknown fields are tolerated at every level", func(t *testing.T) {
		t.Parallel()
		doc := `{"future_top":123,"header":{"version":"9.9.9","new_header_field":{"a":1},` +
			`"stats":{"group_count":1,"total_file_count":2,"total_file_size":20,` +
			`"redundant_file_count":1,"redundant_file_size":10,"missing_file_count":0,` +
			`"missing_file_size":0,"future_stat":7}},` +
			`"groups":[{"file_len":10,"file_hash":"h","files":["/k","/d"],"future_group_field":true}]}`
		got, err := parsing.DecodeReport(strings.NewReader(doc), 100)
		if err != nil {
			t.Fatalf("DecodeReport with unknown fields: %v", err)
		}
		if got.TotalGroups != 1 || got.Groups[0].Keeper != "/k" {
			t.Errorf("got %+v, want the known fields decoded", got)
		}
	})

	t.Run("groups before header decodes order-independently", func(t *testing.T) {
		t.Parallel()
		doc := `{"groups":[{"file_len":10,"file_hash":"h","files":["/k","/d"]}],` +
			`"header":{"stats":` + statsJSON(1, 1, 10) + `}}`
		got, err := parsing.DecodeReport(strings.NewReader(doc), 100)
		if err != nil {
			t.Fatalf("DecodeReport: %v", err)
		}
		if got.TotalGroups != 1 || got.Stats.GroupCount != 1 {
			t.Errorf("got %+v, want one group with matching stats", got)
		}
	})
}

func TestDecodeReport_errors(t *testing.T) {
	t.Parallel()
	oneGroup := `[{"file_len":10,"file_hash":"h","files":["/k","/d"]}]`

	tests := []struct {
		name    string
		doc     string
		wantSub string
	}{
		{
			name:    "text report is not JSON",
			doc:     "# Redundant: 5 files (1.2 GB)\n# Total: 10 3 groups\n",
			wantSub: "report",
		},
		{
			name:    "truncated document",
			doc:     reportDoc(statsJSON(1, 1, 10), oneGroup)[:40],
			wantSub: "report",
		},
		{
			name:    "missing header",
			doc:     `{"groups":` + oneGroup + `}`,
			wantSub: "missing header",
		},
		{
			name:    "null stats",
			doc:     reportDoc("null", oneGroup),
			wantSub: "no stats",
		},
		{
			name:    "absent stats field",
			doc:     `{"header":{"version":"0.35.0"},"groups":` + oneGroup + `}`,
			wantSub: "no stats",
		},
		{
			name:    "missing groups array",
			doc:     `{"header":{"stats":` + statsJSON(0, 0, 0) + `}}`,
			wantSub: "missing groups",
		},
		{
			name:    "single-file group",
			doc:     reportDoc(statsJSON(1, 0, 0), `[{"file_len":10,"file_hash":"h","files":["/only"]}]`),
			wantSub: "at least a keeper",
		},
		{
			name:    "empty files group",
			doc:     reportDoc(statsJSON(1, 0, 0), `[{"file_len":10,"file_hash":"h","files":[]}]`),
			wantSub: "at least a keeper",
		},
		{
			name:    "negative file_len",
			doc:     reportDoc(statsJSON(1, 1, 10), `[{"file_len":-5,"file_hash":"h","files":["/k","/d"]}]`),
			wantSub: "negative file_len",
		},
		{
			name:    "header count disagrees with document",
			doc:     reportDoc(statsJSON(5, 1, 10), oneGroup),
			wantSub: "claim 5 groups but the document carries 1",
		},
		{
			name:    "top-level array",
			doc:     `[]`,
			wantSub: "report",
		},
		{
			name:    "groups is an object",
			doc:     reportDoc(statsJSON(0, 0, 0), `{}`),
			wantSub: "groups",
		},
		{
			// A null where the groups array belongs is upstream drift, not
			// json.Unmarshal's nil-slice tolerance: the walk fails loudly.
			name:    "null groups array",
			doc:     reportDoc(statsJSON(0, 0, 0), `null`),
			wantSub: "unexpected null",
		},
		{
			// A null document walks as an empty object (no keys), so the
			// missing-header cross-check rejects it.
			name:    "null document",
			doc:     `null`,
			wantSub: "missing header",
		},
		{
			// Anything after the top-level object's closing brace is
			// rejected (the decoder's whole-input strictness): a
			// concatenated second document must not be silently ignored.
			name:    "trailing data after the document",
			doc:     reportDoc(statsJSON(1, 1, 10), oneGroup) + `{"second":true}`,
			wantSub: "trailing data",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parsing.DecodeReport(strings.NewReader(tt.doc), 100)
			if err == nil {
				t.Fatalf("DecodeReport(%q) = nil error, want one containing %q", tt.doc, tt.wantSub)
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantSub)
			}
			if got.TotalGroups != 0 || got.TotalDuplicates != 0 || len(got.Groups) != 0 || got.Stats != (parsing.ReportStats{}) {
				t.Errorf("error path returned non-zero Report %+v, want zero value", got)
			}
		})
	}
}

// TestProperty_DecodeReportRoundTrip generates arbitrary well-formed reports,
// encodes them in the upstream wire shape, and asserts DecodeReport recovers
// the exact totals, retention-capped groups, and keeper mapping.
func TestProperty_DecodeReportRoundTrip(t *testing.T) {
	t.Parallel()

	type wireGroup struct {
		FileLen  int64    `json:"file_len"`
		FileHash string   `json:"file_hash"`
		Files    []string `json:"files"`
	}

	rapid.Check(t, func(rt *rapid.T) {
		nGroups := rapid.IntRange(0, 20).Draw(rt, "nGroups")
		keep := rapid.IntRange(0, 25).Draw(rt, "keep")

		groups := make([]wireGroup, 0, nGroups)
		wantDups := 0
		for i := range nGroups {
			nFiles := rapid.IntRange(2, 6).Draw(rt, "nFiles")
			files := make([]string, 0, nFiles)
			for j := range nFiles {
				// Arbitrary path strings, including delimiter-laden ones: JSON
				// escaping must round-trip them verbatim.
				files = append(files, rapid.StringMatching(`/[ -~]{0,30}`).Draw(rt, "path")+
					jsonInt(i)+"_"+jsonInt(j))
			}
			groups = append(groups, wireGroup{
				FileLen:  rapid.Int64Range(0, 1<<40).Draw(rt, "fileLen"),
				FileHash: "h",
				Files:    files,
			})
			wantDups += nFiles - 1
		}

		doc := map[string]any{
			"header": map[string]any{
				"version": "0.35.0",
				"stats": map[string]any{
					"group_count":          nGroups,
					"total_file_count":     0,
					"total_file_size":      0,
					"redundant_file_count": wantDups,
					"redundant_file_size":  0,
					"missing_file_count":   0,
					"missing_file_size":    0,
				},
			},
			"groups": groups,
		}
		encoded, err := json.Marshal(doc)
		if err != nil {
			rt.Fatalf("marshal fixture: %v", err)
		}

		got, err := parsing.DecodeReport(strings.NewReader(string(encoded)), keep)
		if err != nil {
			rt.Fatalf("DecodeReport: %v", err)
		}
		if got.TotalGroups != nGroups || got.TotalDuplicates != wantDups {
			rt.Fatalf("totals = %d/%d, want %d/%d", got.TotalGroups, got.TotalDuplicates, nGroups, wantDups)
		}
		wantRetained := min(nGroups, keep)
		if len(got.Groups) != wantRetained {
			rt.Fatalf("retained = %d, want %d", len(got.Groups), wantRetained)
		}
		for i, g := range got.Groups {
			if g.Keeper != groups[i].Files[0] {
				rt.Fatalf("group %d keeper = %q, want %q", i, g.Keeper, groups[i].Files[0])
			}
			if len(g.Duplicates) != len(groups[i].Files)-1 {
				rt.Fatalf("group %d duplicates = %d, want %d", i, len(g.Duplicates), len(groups[i].Files)-1)
			}
		}
	})
}

func TestParseActionSummary(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		input          string
		wantRawContain string
		wantFiles      int
		wantReclaimed  int64
		wantEstimated  bool
		wantMatched    bool
	}{
		{
			name:           "standard Processed line",
			input:          "some output\nProcessed 5 files and reclaimed 1.2 GB space\nmore output",
			wantFiles:      5,
			wantReclaimed:  1_200_000_000,
			wantRawContain: "Processed 5 files",
			wantMatched:    true,
		},
		{
			name:           "zero files zero bytes",
			input:          "Processed 0 files and reclaimed 0 B space\n",
			wantFiles:      0,
			wantReclaimed:  0,
			wantRawContain: "Processed 0 files",
			wantMatched:    true,
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
			wantMatched:    true,
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
			wantMatched:    true,
		},
		{
			name:           "first Processed match wins",
			input:          "Processed 5 files and reclaimed 1 GB space\nProcessed 10 files and reclaimed 2 GB space\n",
			wantFiles:      5,
			wantReclaimed:  1_000_000_000,
			wantRawContain: "Processed 5 files",
			wantMatched:    true,
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
			wantMatched:    true,
		},
		{
			name:           "negative file count is rejected, reclaim still parsed",
			input:          "Processed -3 files and reclaimed 4 B space",
			wantFiles:      0,
			wantReclaimed:  4,
			wantRawContain: "Processed",
			wantMatched:    true,
		},
		{
			name:           "dedupe reclaimed up to is parsed as an upper-bound estimate",
			input:          "Processed 73 files and reclaimed up to 512 MB space",
			wantFiles:      73,
			wantReclaimed:  512_000_000,
			wantRawContain: "reclaimed up to",
			wantEstimated:  true,
			wantMatched:    true,
		},
		{
			name:           "reclaimed up to truncated yields files but zero reclaimed",
			input:          "Processed 3 files and reclaimed up to",
			wantFiles:      3,
			wantReclaimed:  0,
			wantRawContain: "Processed 3 files",
			wantEstimated:  true,
			wantMatched:    true,
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
			if got.Estimated != tt.wantEstimated {
				t.Errorf("Estimated = %v, want %v", got.Estimated, tt.wantEstimated)
			}
			if got.Matched != tt.wantMatched {
				t.Errorf("Matched = %v, want %v", got.Matched, tt.wantMatched)
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
			a.RawLine != b.RawLine || a.Estimated != b.Estimated || a.Matched != b.Matched {
			rt.Fatalf("ParseActionSummary(%q) non-deterministic: %+v vs %+v", input, a, b)
		}
	})
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

func TestParseActionSummary_estimatedExactlyNineFields(t *testing.T) {
	t.Parallel()

	// given a dedupe "up to" line with exactly nine fields: the reclaimed
	// number and unit are the last two tokens (no trailing "space"), so
	// len(fields) equals numIdx+2 on the estimated path
	const stdout = "Processed 73 files and reclaimed up to 512 MB"

	// when the action summary is parsed
	got := parsing.ParseActionSummary(stdout)

	// then the exact-length line is let through and the upper-bound size is
	// parsed; a wider length guard would drop the reclaimed total to zero
	if got.Files != 73 {
		t.Errorf("ParseActionSummary(%q).Files = %d, want 73", stdout, got.Files)
	}
	if got.ReclaimedBytes != 512_000_000 {
		t.Errorf("ParseActionSummary(%q).ReclaimedBytes = %d, want 512000000", stdout, got.ReclaimedBytes)
	}
	if !got.Estimated {
		t.Errorf("ParseActionSummary(%q).Estimated = %v, want true", stdout, got.Estimated)
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
		{"petabytes", "1 PB", 1_000_000_000_000_000},
		{"exabytes", "1 EB", 1_000_000_000_000_000_000},
		{"zero kilobytes", "0 KB", 0},
		{"single-letter K alias", "5 K", 5_000},
		{"single-letter M alias", "5 M", 5_000_000},
		{"single-letter G alias", "2 G", 2_000_000_000},
		{"single-letter T alias", "3 T", 3_000_000_000_000},
		{"single-letter P alias", "2 P", 2_000_000_000_000_000},
		{"single-letter E alias", "2 E", 2_000_000_000_000_000_000},
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

// TestParseHumanBytes_IECUnitsRejected pins the deliberate deletion of the
// IEC binary-unit tolerance: the pinned fclones (bytesize 1.3.0) renders
// decimal SI units only, so KiB..EiB parse to 0 like any unknown unit, and a
// future upstream switch to IEC surfaces as the action-summary drift warning
// instead of being silently absorbed by a unit table nothing exercises.
func TestParseHumanBytes_IECUnitsRejected(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"1 KiB", "1 MiB", "1.5 GiB", "1 TiB", "1 PiB", "1 EiB", "1 kib"} {
		if got := parsing.ParseHumanBytes(in); got != 0 {
			t.Errorf("ParseHumanBytes(%q) = %d, want 0 (IEC units are deliberately not accepted)", in, got)
		}
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
