// Package parsing extracts structured data from fclones command output: the
// JSON scan report (exact header statistics plus duplicate groups, decoded
// strictly and streamed so report size never pressures memory) and the text
// action summary (files processed and bytes reclaimed). The report decode
// fails loudly on anything malformed -- degraded stats never masquerade as a
// healthy zero -- while the summary parser tolerates drift and flags an
// unrecognized summary via ActionSummary.Matched.
package parsing

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// Report is a decoded fclones JSON scan report: the header's exact numeric
// statistics, the first groups mapped into display form for per-pair logging,
// and the full-document totals counted while streaming.
type Report struct {
	// Groups holds at most the keepGroups first duplicate groups, mapped
	// into display form (keeper, duplicates, human-readable size).
	Groups []DuplicateGroup
	// Stats is the report header's statistics block (never nil-derived:
	// DecodeReport rejects a report whose header carries no stats).
	Stats ReportStats
	// TotalGroups counts every group in the document, including those
	// beyond the keepGroups retention cap.
	TotalGroups int
	// TotalDuplicates counts every duplicate (non-keeper) file across all
	// groups in the document, including those beyond the retention cap.
	TotalDuplicates int
}

// ReportStats carries the fclones report-header statistics. Upstream
// (fclones FileStats, serialized by serde as snake_case bare integers)
// declares the container Option<FileStats>; DecodeReport rejects the
// null/absent case, so consumers always see concrete values.
type ReportStats struct {
	GroupCount         int   `json:"group_count"`
	TotalFileCount     int   `json:"total_file_count"`
	TotalFileSize      int64 `json:"total_file_size"`
	RedundantFileCount int   `json:"redundant_file_count"`
	RedundantFileSize  int64 `json:"redundant_file_size"`
	MissingFileCount   int   `json:"missing_file_count"`
	MissingFileSize    int64 `json:"missing_file_size"`
}

// reportHeaderJSON mirrors the fclones ReportHeader fields the decoder
// consumes. Stats is a pointer because upstream declares it
// Option<FileStats>: null and absent are real wire states, and both are
// decode errors here.
type reportHeaderJSON struct {
	Stats *ReportStats `json:"stats"`
}

// reportGroupJSON mirrors one fclones FileGroup entry. file_hash is
// deliberately not consumed.
type reportGroupJSON struct {
	Files   []string `json:"files"`
	FileLen int64    `json:"file_len"`
}

// DuplicateGroup is a scan-report group in display form: one keeper file
// plus its duplicates, with the per-file size rendered for log lines.
type DuplicateGroup struct {
	Keeper     string
	SizePerDup string
	Duplicates []string
}

// DecodeReport strictly decodes an fclones JSON report (`fclones group -f
// json`) from r, streaming the groups array element by element so memory is
// bounded by the retained groups plus one in-flight group, never by report
// size. keepGroups caps how many groups are retained in Report.Groups for
// per-pair logging (non-positive retains none); totals are counted across
// the whole document regardless.
//
// Strictness contract: a malformed or truncated document, a missing header,
// an absent or null header.stats, a missing groups array, a group with fewer
// than two files or a negative file_len, or a header group_count that
// disagrees with the streamed group count is an error -- the caller fails
// the run loudly. Unknown fields at any level are tolerated so additive
// upstream changes do not break the wrapper. The keeper is files[0]
// (fclones' own keep-first semantics).
//
// Wire shape verified against pinned upstream fclones v0.35.0
// (fclones/src/report.rs: SerializableReport{header, groups},
// ReportHeader.stats Option<FileStats>, FileGroup{file_len, file_hash,
// files}); the format is stable since fclones 0.18. Re-verify on every
// FCLONES_VERSION bump (see CONTRIBUTING).
func DecodeReport(r io.Reader, keepGroups int) (Report, error) {
	dec := json.NewDecoder(r)

	if err := expectDelim(dec, json.Delim('{')); err != nil {
		return Report{}, fmt.Errorf("report: %w", err)
	}
	header, report, groupsSeen, err := decodeReportBody(dec, keepGroups)
	if err != nil {
		return Report{}, err
	}
	if err := expectDelim(dec, json.Delim('}')); err != nil {
		return Report{}, fmt.Errorf("report: %w", err)
	}

	switch {
	case header == nil:
		return Report{}, errors.New("report: missing header")
	case header.Stats == nil:
		return Report{}, errors.New("report: header carries no stats (null or absent)")
	case !groupsSeen:
		return Report{}, errors.New("report: missing groups array")
	case header.Stats.GroupCount != report.TotalGroups:
		return Report{}, fmt.Errorf("report: header stats claim %d groups but the document carries %d",
			header.Stats.GroupCount, report.TotalGroups)
	}

	report.Stats = *header.Stats
	return report, nil
}

// reportDecoder carries the mutable accounting for decodeReportBody as it
// walks the top-level object's key/value pairs.
type reportDecoder struct {
	header     *reportHeaderJSON
	report     Report
	keepGroups int
	groupsSeen bool
}

// field dispatches one top-level key: the header is decoded eagerly (it is
// small), the groups array is streamed via decodeGroups, and unknown fields
// are skipped so additive upstream changes do not break the wrapper.
func (d *reportDecoder) field(dec *json.Decoder, key string) error {
	switch key {
	case "header":
		d.header = new(reportHeaderJSON)
		if err := dec.Decode(d.header); err != nil {
			return fmt.Errorf("report header: %w", err)
		}
	case "groups":
		d.groupsSeen = true
		return decodeGroups(dec, d.keepGroups, &d.report)
	default:
		// Unknown top-level fields are tolerated: skip the value.
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return fmt.Errorf("report field %q: %w", key, err)
		}
	}
	return nil
}

// decodeReportBody consumes the top-level object's key/value pairs into a
// reportDecoder and returns its accounting.
func decodeReportBody(dec *json.Decoder, keepGroups int) (header *reportHeaderJSON, report Report, groupsSeen bool, err error) {
	d := reportDecoder{keepGroups: keepGroups}
	for dec.More() {
		keyTok, tokErr := dec.Token()
		if tokErr != nil {
			return nil, Report{}, false, fmt.Errorf("report key: %w", tokErr)
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, Report{}, false, fmt.Errorf("report: unexpected token %v where an object key was expected", keyTok)
		}
		if fieldErr := d.field(dec, key); fieldErr != nil {
			return nil, Report{}, false, fieldErr
		}
	}
	return d.header, d.report, d.groupsSeen, nil
}

// decodeGroups streams the groups array, retaining at most keepGroups mapped
// groups in report.Groups while counting full-document totals, and validates
// each group's structure as it passes.
func decodeGroups(dec *json.Decoder, keepGroups int, report *Report) error {
	if err := expectDelim(dec, json.Delim('[')); err != nil {
		return fmt.Errorf("report groups: %w", err)
	}
	for dec.More() {
		var g reportGroupJSON
		if err := dec.Decode(&g); err != nil {
			return fmt.Errorf("report group %d: %w", report.TotalGroups, err)
		}
		if len(g.Files) < 2 {
			return fmt.Errorf("report group %d: %d files, want at least a keeper and one duplicate",
				report.TotalGroups, len(g.Files))
		}
		if g.FileLen < 0 {
			return fmt.Errorf("report group %d: negative file_len %d", report.TotalGroups, g.FileLen)
		}
		if len(report.Groups) < keepGroups {
			report.Groups = append(report.Groups, DuplicateGroup{
				Keeper:     g.Files[0],
				SizePerDup: HumanBytes(g.FileLen),
				Duplicates: g.Files[1:],
			})
		}
		report.TotalGroups++
		report.TotalDuplicates += len(g.Files) - 1
	}
	if err := expectDelim(dec, json.Delim(']')); err != nil {
		return fmt.Errorf("report groups: %w", err)
	}
	return nil
}

// expectDelim consumes one token from dec and verifies it is the wanted
// structural delimiter.
func expectDelim(dec *json.Decoder, want json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != want {
		return fmt.Errorf("unexpected token %v, want %q", tok, want)
	}
	return nil
}

// ActionSummary holds the parsed "Processed N files and reclaimed X Y space"
// line from fclones action output.
type ActionSummary struct {
	RawLine        string
	Files          int
	ReclaimedBytes int64
	// Estimated is true when fclones reported the reclaimed total as an upper
	// bound ("reclaimed up to X"). The dedupe action does this because the
	// dedup ioctl is advisory; link and remove report an exact figure.
	Estimated bool
	// Matched is true when a recognizable "Processed ... reclaimed ..."
	// summary line was found and Files/ReclaimedBytes/Estimated were parsed
	// from it. It is false on the fallback path, where RawLine carries the
	// last non-empty output line (or nothing): there the zero metrics mean
	// "no summary recognized", not "nothing reclaimed".
	Matched bool
}

// reclaimedMetrics parses the file count and reclaimed byte total from a trimmed
// "Processed <files> files and reclaimed [up to ]<num> <unit> [space]" line.
// fields[1] is the file count. The reclaimed <num> <unit> normally sit at
// fields[5..6]; the dedupe action prints "reclaimed up to <num> <unit>" (its
// reclaim is an advisory upper bound), which shifts them to fields[7..8] and
// sets estimated. The >= 7 check guarantees fields[5..6] exist for the exact
// form; the shifted form is bounds-checked before fields[7..8] are read. A
// non-numeric or negative file count leaves files at 0 while the reclaimed size
// is parsed independently.
func reclaimedMetrics(rawLine string) (files int, reclaimedBytes int64, estimated bool) {
	fields := strings.Fields(rawLine)
	if len(fields) < 7 {
		return 0, 0, false
	}
	if n, err := strconv.Atoi(fields[1]); err == nil && n >= 0 {
		files = n
	}
	numIdx := 5
	if fields[5] == "up" && fields[6] == "to" {
		estimated = true
		numIdx = 7
		if len(fields) < numIdx+2 {
			return files, 0, estimated
		}
	}
	return files, parseHumanBytes(fields[numIdx] + " " + fields[numIdx+1]), estimated
}

// ParseActionSummary extracts structured metrics from fclones action stdout.
func ParseActionSummary(stdout string) ActionSummary {
	var summary ActionSummary
	var lastNonEmpty string

	for line := range strings.SplitSeq(stdout, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lastNonEmpty = trimmed
		if idx := strings.Index(line, "Processed"); idx != -1 &&
			strings.Contains(line, "reclaimed") {
			summary.RawLine = strings.TrimSpace(line[idx:])
			summary.Files, summary.ReclaimedBytes, summary.Estimated = reclaimedMetrics(summary.RawLine)
			summary.Matched = true
			return summary
		}
	}

	if summary.RawLine == "" && lastNonEmpty != "" {
		summary.RawLine = lastNonEmpty
	}
	return summary
}

// byteUnitMultipliers maps an upper-cased size unit to its byte multiplier.
// fclones (via bytesize 1.3.0, the pinned version) renders DECIMAL SI units
// (KB..EB) -- the same system HumanBytes emits, so the parse and format sides
// agree. Re-verify the unit system on FCLONES_VERSION bumps (CONTRIBUTING);
// an unrecognized unit parses to 0, which the action-summary drift warning
// then surfaces.
var byteUnitMultipliers = map[string]int64{
	"B":  1,
	"KB": 1_000, "K": 1_000,
	"MB": 1_000_000, "M": 1_000_000,
	"GB": 1_000_000_000, "G": 1_000_000_000,
	"TB": 1_000_000_000_000, "T": 1_000_000_000_000,
	"PB": 1_000_000_000_000_000, "P": 1_000_000_000_000_000,
	"EB": 1_000_000_000_000_000_000, "E": 1_000_000_000_000_000_000,
}

// parseHumanBytes converts "<num> <unit>" (e.g. "1.5 MB", "512 B") into a
// byte count. Returns 0 on any parse failure.
func parseHumanBytes(s string) int64 {
	fields := strings.Fields(s)
	if len(fields) != 2 {
		return 0
	}
	num, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	mult, ok := byteUnitMultipliers[strings.ToUpper(fields[1])]
	if !ok {
		return 0
	}
	return floatToInt64(num * float64(mult))
}

// floatToInt64 converts a byte-count float64 to int64, returning 0 for NaN,
// negative, or out-of-int64-range values. The range is checked BEFORE the
// conversion because converting an out-of-range float64 to int64 is
// implementation-defined (amd64 wraps to MinInt64, arm64 saturates to
// MaxInt64), so a post-conversion sign check is not portable. This also
// rejects the Inf/NaN literals that strconv.ParseFloat accepts.
func floatToInt64(f float64) int64 {
	if math.IsNaN(f) || f < 0 || f >= float64(math.MaxInt64) {
		return 0
	}
	return int64(f)
}

// humanByteSuffixes are the SI-decimal suffixes HumanBytes emits, ordered
// by ascending magnitude. Package-level (like byteUnitMultipliers) so the
// table is not rebuilt on every call.
var humanByteSuffixes = []string{"KB", "MB", "GB", "TB", "PB", "EB"}

// HumanBytes formats a byte count as a short SI-unit string for log lines.
func HumanBytes(n int64) string {
	const unit = int64(1_000)
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := unit, 0
	for x := n / unit; x >= unit && exp < len(humanByteSuffixes)-1; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %s", float64(n)/float64(div), humanByteSuffixes[exp])
}
