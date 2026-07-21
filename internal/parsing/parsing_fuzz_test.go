package parsing_test

import (
	"math"
	"strings"
	"testing"

	"github.com/cplieger/docker-fclones-scheduler/internal/parsing"
)

func FuzzDecodeReport(f *testing.F) {
	valid := `{"header":{"version":"0.35.0","stats":{"group_count":1,` +
		`"total_file_count":2,"total_file_size":20,"redundant_file_count":1,` +
		`"redundant_file_size":10,"missing_file_count":0,"missing_file_size":0}},` +
		`"groups":[{"file_len":10,"file_hash":"h","files":["/k","/d"]}]}`
	f.Add(valid, 100)
	f.Add(valid[:40], 100)                                         // truncated
	f.Add(`{"header":{"stats":null},"groups":[]}`, 1)              // null stats
	f.Add(`{"groups":[]}`, 0)                                      // missing header
	f.Add(`{"header":{"stats":{"group_count":9}},"groups":[]}`, 5) // count mismatch
	f.Add("# Redundant: 5 files (1.2 GB)\n", 100)                  // old text format
	f.Add(valid+`{"second":true}`, 100)                            // trailing data rejected
	f.Add(`{"header":{"stats":null},"groups":null}`, 1)            // null groups rejected
	f.Add("", -1)
	f.Fuzz(func(t *testing.T, doc string, keep int) {
		got, err := parsing.DecodeReport(strings.NewReader(doc), keep)
		if err != nil {
			// Loud-failure contract: an error never leaks partial output.
			if got.TotalGroups != 0 || got.TotalDuplicates != 0 ||
				len(got.Groups) != 0 || got.Stats != (parsing.ReportStats{}) {
				t.Fatalf("error path returned non-zero Report %+v: doc=%q", got, doc)
			}
			return
		}
		// Success invariants: the header count matches the streamed count,
		// retention respects the cap, and every retained group is structurally
		// sound (keep-first keeper plus at least one duplicate).
		if got.Stats.GroupCount != got.TotalGroups {
			t.Fatalf("GroupCount %d != TotalGroups %d: doc=%q", got.Stats.GroupCount, got.TotalGroups, doc)
		}
		if keep >= 0 && len(got.Groups) > keep {
			t.Fatalf("retained %d groups exceeds keep=%d: doc=%q", len(got.Groups), keep, doc)
		}
		if len(got.Groups) > got.TotalGroups {
			t.Fatalf("retained %d groups exceeds total %d: doc=%q", len(got.Groups), got.TotalGroups, doc)
		}
		if got.TotalDuplicates < got.TotalGroups {
			t.Fatalf("TotalDuplicates %d < TotalGroups %d (every group has >= 1 duplicate): doc=%q",
				got.TotalDuplicates, got.TotalGroups, doc)
		}
		for i, g := range got.Groups {
			if g.Keeper == "" && len(g.Duplicates) == 0 {
				t.Fatalf("group %d empty: doc=%q", i, doc)
			}
			if len(g.Duplicates) < 1 {
				t.Fatalf("group %d has no duplicates: doc=%q", i, doc)
			}
		}
	})
}

func FuzzParseActionSummary(f *testing.F) {
	f.Add("some output\nProcessed 5 files and reclaimed 1.2 GB space\nmore output")
	f.Add("Processed 0 files and reclaimed 0 B space\n")
	f.Add("Processed 73 files and reclaimed up to 512 MB space")
	f.Add("Processed 3 files and reclaimed up to")
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
		// Matched is the metrics' provenance flag: metrics and Estimated are
		// parsed only from a recognized summary line, so on the fallback path
		// (Matched false) all of them must be zero values. A matched summary's
		// RawLine must carry the anchors the match keyed on.
		if !summary.Matched && (summary.Files != 0 || summary.ReclaimedBytes != 0 || summary.Estimated) {
			t.Fatalf("Matched=false but metrics set: %+v input=%q", summary, input)
		}
		// (Only the "Processed" anchor is guaranteed in RawLine: the match's
		// "reclaimed" anchor is tested against the whole source line, which
		// may place it before the "Processed" offset RawLine starts at.)
		if summary.Matched && !strings.HasPrefix(summary.RawLine, "Processed") {
			t.Fatalf("Matched=true but RawLine does not start at the match anchor: %q input=%q",
				summary.RawLine, input)
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

func FuzzParseHumanBytes(f *testing.F) {
	f.Add("1.5 MB")
	f.Add("512 B")
	f.Add("0 KB")
	f.Add("3.7 TB")
	f.Add("garbage")
	f.Add("")
	// Guard regression seeds: NaN/Inf/overflow/negative all clamp to 0, and
	// the deleted IEC tolerance stays deleted (KiB parses to 0).
	f.Add("Inf KB")
	f.Add("-Inf KB")
	f.Add("NaN MB")
	f.Add("-5 KB")
	f.Add("99999999 TB")
	f.Add("1 KiB")
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
