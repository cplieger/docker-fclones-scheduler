package parsing

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// DefaultSizeStr is the fallback size string when no duplicates are found.
const DefaultSizeStr = "0 B"

// Stats holds parsed statistics from fclones output.
type Stats struct {
	Groups string
	Size   string
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
func ParseStats(output string) Stats {
	stats := Stats{Groups: "0", Size: DefaultSizeStr}

	for line := range strings.SplitSeq(output, "\n") {
		switch {
		case strings.HasPrefix(line, "# Redundant:"):
			stats.Size = parseRedundantSize(line)
		case strings.HasPrefix(line, "# Total:"):
			if parts := strings.Fields(line); len(parts) >= 2 && parts[len(parts)-1] == "groups" {
				stats.Groups = parts[len(parts)-2]
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
	return DefaultSizeStr
}

// isGroupHeader reports whether a line is an fclones group header like
// "3a2b, 512 B * 2:" (comma, star, trailing colon).
func isGroupHeader(line string) bool {
	return strings.Contains(line, ",") &&
		strings.Contains(line, "*") &&
		strings.HasSuffix(line, ":")
}

// ParseDuplicateGroups parses an fclones custom-format report into structured
// groups. Each group header looks like:
//
//	3a2b, 512 B * 2:
//
// followed by one path per line (one "keeper" and one or more duplicates),
// terminated by a blank line.
func ParseDuplicateGroups(report string) []DuplicateGroup {
	var groups []DuplicateGroup
	var current DuplicateGroup
	inGroup := false

	flush := func() {
		if inGroup && len(current.Duplicates) > 0 {
			groups = append(groups, current)
		}
		current = DuplicateGroup{}
		inGroup = false
	}

	for line := range strings.SplitSeq(report, "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			flush()
			continue
		}
		if !inGroup {
			if isGroupHeader(line) {
				current.SizePerDup = extractGroupSize(line)
				inGroup = true
			}
			continue
		}
		// Once in a group, every non-blank line is a path until the blank-line
		// flush ends the group. Header detection is deliberately NOT re-run here:
		// fclones separates groups with a blank line, so a duplicate filename that
		// embeds the header delimiters (',', '*', trailing ':') must not be
		// reclassified as a new group header. Do not add an isGroupHeader check in
		// this branch.
		if current.Keeper == "" {
			current.Keeper = trimmed
		} else {
			current.Duplicates = append(current.Duplicates, trimmed)
		}
	}
	flush()
	return groups
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
			fields := strings.Fields(summary.RawLine)
			// Token positions in the documented shape
			// "Processed <files> files and reclaimed <num> <unit> space":
			// fields[1]=file count, fields[5]=reclaimed number, fields[6]=unit.
			// The >= 7 guard guarantees fields[6] exists; the trailing "space"
			// word is optional, so a 7-field line still parses.
			if len(fields) >= 7 {
				if n, err := strconv.Atoi(fields[1]); err == nil && n >= 0 {
					summary.Files = n
				}
				summary.ReclaimedBytes = parseHumanBytes(fields[5] + " " + fields[6])
			}
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
// defensively so ParseHumanBytes still parses correctly if a future
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
