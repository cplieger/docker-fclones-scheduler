package parsing

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// defaultSizeStr is the fallback size string when no duplicates are found.
const defaultSizeStr = "0 B"

// Stats holds parsed statistics from fclones output.
type Stats struct {
	Groups string
	Size   string
	// TotalParsed reports whether a recognizable "# Total: ... N groups" line
	// was found. It is false when that line is absent or its format changed;
	// the count token in Groups may still be non-numeric when it is true.
	// Callers use it to tell "fclones reported 0 groups" apart from "we could
	// not read the group count at all" -- the latter is output-format drift.
	TotalParsed bool
}

// DuplicateGroup is a parsed fclones report group: one keeper file plus its
// duplicates, with the per-file size echoed from the group header for display.
type DuplicateGroup struct {
	Keeper     string
	SizePerDup string
	Duplicates []string
}

// ActionSummary holds the parsed "Processed N files and reclaimed X Y space"
// line from fclones action output.
type ActionSummary struct {
	RawLine        string
	Files          int
	ReclaimedBytes int64
}

// ParseStats extracts statistics from fclones output.
//
// The output embeds scanned filenames, which are attacker-influenceable and may contain
// newlines; a crafted path can therefore inject a line that looks like a "# Total:" or
// "# Redundant:" stat. The returned Stats are advisory (logging and Grafana alerting
// only) and never drive file operations, so this is an accepted log-integrity tradeoff.
func ParseStats(output string) Stats {
	stats := Stats{Groups: "0", Size: defaultSizeStr}

	for line := range strings.SplitSeq(output, "\n") {
		switch {
		case strings.HasPrefix(line, "# Redundant:"):
			stats.Size = parseRedundantSize(line)
		case strings.HasPrefix(line, "# Total:"):
			if parts := strings.Fields(line); len(parts) >= 2 && parts[len(parts)-1] == "groups" {
				stats.Groups = parts[len(parts)-2]
				stats.TotalParsed = true
			}
		}
	}

	return stats
}

// parseRedundantSize extracts the human-readable size from a "# Redundant:" line.
// Prefers the parenthesized form "(1.2 GB)" over the bare "512 MB" form.
func parseRedundantSize(line string) string {
	if start := strings.Index(line, "("); start != -1 {
		if end := strings.Index(line, ")"); end > start {
			if s := line[start+1 : end]; s != "" {
				return s
			}
		}
	}
	if parts := strings.Fields(line); len(parts) >= 4 {
		return parts[2] + " " + parts[3]
	}
	return defaultSizeStr
}

// isGroupHeader reports whether a line is an fclones group header like
// "3a2b, 512 B * 2:" (comma, star, trailing colon).
func isGroupHeader(line string) bool {
	return strings.Contains(line, ",") &&
		strings.Contains(line, "*") &&
		strings.HasSuffix(line, ":")
}

// groupParser carries the mutable accounting for ParseDuplicateGroups as it
// scans an fclones report line by line.
type groupParser struct {
	groups  []DuplicateGroup
	current DuplicateGroup
	inGroup bool
}

// flush finalizes the current group, keeping it only when it collected at
// least one duplicate, then resets the accumulator for the next group.
func (p *groupParser) flush() {
	if p.inGroup && len(p.current.Duplicates) > 0 {
		p.groups = append(p.groups, p.current)
	}
	p.current = DuplicateGroup{}
	p.inGroup = false
}

// step feeds one report line into the parser, updating its accounting.
func (p *groupParser) step(line string) {
	if strings.HasPrefix(line, "#") {
		return
	}
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		p.flush()
		return
	}
	if !p.inGroup {
		if isGroupHeader(line) {
			p.current.SizePerDup = extractGroupSize(line)
			p.inGroup = true
		}
		return
	}
	// Once in a group, every non-blank line is a path until the blank-line
	// flush ends the group. Header detection is deliberately NOT re-run here:
	// fclones separates groups with a blank line, so a duplicate filename that
	// embeds the header delimiters (',', '*', trailing ':') must not be
	// reclassified as a new group header. Do not add an isGroupHeader check in
	// this branch.
	if p.current.Keeper == "" {
		p.current.Keeper = trimmed
	} else {
		p.current.Duplicates = append(p.current.Duplicates, trimmed)
	}
}

// ParseDuplicateGroups parses an fclones custom-format report into structured
// groups. Each group header looks like:
//
//	3a2b, 512 B * 2:
//
// followed by one path per line (one "keeper" and one or more duplicates),
// terminated by a blank line.
func ParseDuplicateGroups(report string) []DuplicateGroup {
	var p groupParser
	for line := range strings.SplitSeq(report, "\n") {
		p.step(line)
	}
	p.flush()
	return p.groups
}

// extractGroupSize pulls the single-file size from a group header.
func extractGroupSize(header string) string {
	h := strings.TrimSuffix(header, ":")
	if idx := strings.LastIndex(h, " * "); idx != -1 {
		h = h[:idx]
	}
	if idx := strings.Index(h, ","); idx != -1 {
		h = h[idx+1:]
	}
	return strings.TrimSpace(h)
}

// reclaimedMetrics parses the file count and reclaimed byte total from a
// trimmed "Processed <files> files and reclaimed <num> <unit> [space]" line.
// Token positions in that documented shape: fields[1]=file count,
// fields[5]=reclaimed number, fields[6]=unit. The >= 7 check guarantees
// fields[6] exists; the trailing "space" word is optional, so a 7-field line
// still parses. A non-numeric or negative file count leaves files at 0 while
// the reclaimed size is parsed independently.
func reclaimedMetrics(rawLine string) (files int, reclaimedBytes int64) {
	fields := strings.Fields(rawLine)
	if len(fields) < 7 {
		return 0, 0
	}
	if n, err := strconv.Atoi(fields[1]); err == nil && n >= 0 {
		files = n
	}
	return files, parseHumanBytes(fields[5] + " " + fields[6])
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
			summary.Files, summary.ReclaimedBytes = reclaimedMetrics(summary.RawLine)
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
// (KB..EB) by default -- the same system HumanBytes emits, so today both the
// parse and format sides agree. The IEC binary units (KiB..EiB) are accepted
// defensively so parseHumanBytes still parses correctly if a future
// fclones/bytesize bump switches its output to IEC.
var byteUnitMultipliers = map[string]int64{
	"B":   1,
	"KIB": 1 << 10, "MIB": 1 << 20, "GIB": 1 << 30,
	"TIB": 1 << 40, "PIB": 1 << 50, "EIB": 1 << 60,
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
