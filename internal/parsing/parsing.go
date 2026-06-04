package parsing

import (
	"fmt"
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
			stats.Size = ParseRedundantSize(line)
		case strings.HasPrefix(line, "# Total:"):
			if parts := strings.Fields(line); len(parts) >= 2 && parts[len(parts)-1] == "groups" {
				stats.Groups = parts[len(parts)-2]
			}
		}
	}

	return stats
}

// ParseRedundantSize extracts the human-readable size from a "# Redundant:" line.
// Prefers the parenthesized form "(1.2 GB)" over the bare "512 MB" form.
func ParseRedundantSize(line string) string {
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

// IsGroupHeader reports whether a line is an fclones group header like
// "3a2b,1024 B,2 * 512 B:" (comma, star, trailing colon).
func IsGroupHeader(line string) bool {
	return strings.Contains(line, ",") &&
		strings.Contains(line, "*") &&
		strings.HasSuffix(line, ":")
}

// ParseDuplicateGroups parses an fclones custom-format report into structured
// groups. Each group header looks like:
//
//	3a2b,1024 B,2 * 512 B:
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
		if IsGroupHeader(line) {
			flush()
			current.SizePerDup = ExtractGroupSize(line)
			inGroup = true
			continue
		}
		if !inGroup {
			continue
		}
		if current.Keeper == "" {
			current.Keeper = trimmed
		} else {
			current.Duplicates = append(current.Duplicates, trimmed)
		}
	}
	flush()
	return groups
}

// ExtractGroupSize pulls the single-file size from a group header.
func ExtractGroupSize(header string) string {
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
			if len(fields) >= 7 {
				if n, err := strconv.Atoi(fields[1]); err == nil {
					summary.Files = n
				}
				summary.ReclaimedBytes = ParseHumanBytes(fields[5] + " " + fields[6])
			}
			return summary
		}
	}

	if summary.RawLine == "" && lastNonEmpty != "" {
		summary.RawLine = lastNonEmpty
	}
	return summary
}

// ParseHumanBytes converts "<num> <unit>" (e.g. "1.5 MB", "512 B") into a
// byte count. Returns 0 on any parse failure.
func ParseHumanBytes(s string) int64 {
	fields := strings.Fields(s)
	if len(fields) != 2 {
		return 0
	}
	num, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	unit := strings.ToUpper(fields[1])
	var mult int64
	switch unit {
	case "B":
		mult = 1
	case "KB", "K":
		mult = 1_000
	case "MB", "M":
		mult = 1_000_000
	case "GB", "G":
		mult = 1_000_000_000
	case "TB", "T":
		mult = 1_000_000_000_000
	default:
		return 0
	}
	result := int64(num * float64(mult))
	if result < 0 {
		return 0 // overflow
	}
	return result
}

// HumanBytes formats a byte count as a short SI-unit string for log lines.
func HumanBytes(n int64) string {
	const unit = int64(1_000)
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := unit, 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	suffix := []string{"KB", "MB", "GB", "TB", "PB"}[exp]
	return fmt.Sprintf("%.1f %s", float64(n)/float64(div), suffix)
}
