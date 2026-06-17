package parsing

// Boundary-characterization tests for the four surviving CONDITIONALS_BOUNDARY
// gremlins mutants in parsing.go:
//
//   parsing.go:43  `len(parts) >= 2` -> `>`   (ParseStats, "# Total:" branch)
//   parsing.go:56  `end > start`     -> `>=`  (ParseRedundantSize)
//   parsing.go:152 `n >= 0`          -> `>`   (ParseActionSummary)
//   parsing.go:195 `result < 0`      -> `<=`  (ParseHumanBytes)
//
// All four are EQUIVALENT mutants; these tests pin the exact boundary contract
// but do not (and cannot) "kill" the mutants -- see the per-test reasoning. They
// add assertions the existing suite did not cover (2-field "# Total:" line,
// ")"-before-"(", a non-zero reclaim with zero files, and a direct
// ParseHumanBytes("0 B")). Internal test package so the analysis lives beside
// the code under test.

import "testing"

// TestGkDockerFclonesSchedulerU1_ParseStatsTotalTwoFieldBoundary pins
// parsing.go:43. The only `len(parts) >= 2` vs `> 2` difference is at
// len(parts) == 2, but the guard is `&& parts[last] == "groups"`, and for any
// line reaching this branch (prefix "# Total:") the second field always begins
// with "Total:" -- never exactly "groups". So the conjunct is false at the
// boundary either way and Groups stays the "0" default. A 4-field line
// ("# Total: 7 groups", the > 2 region) sets Groups to "7" under both
// operators. Hence parsing.go:43 is EQUIVALENT; this only locks the contract.
func TestGkDockerFclonesSchedulerU1_ParseStatsTotalTwoFieldBoundary(t *testing.T) {
	t.Parallel()

	// Exactly two fields ("#", "Total:") -- the len(parts) == 2 boundary.
	if got := ParseStats("# Total:").Groups; got != "0" {
		t.Errorf("ParseStats(%q).Groups = %q, want %q", "# Total:", got, "0")
	}
	// Four fields ("#","Total:","7","groups") -- the len(parts) > 2 region.
	if got := ParseStats("# Total: 7 groups\n").Groups; got != "7" {
		t.Errorf("ParseStats(%q).Groups = %q, want %q", "# Total: 7 groups\\n", got, "7")
	}
}

// TestGkDockerFclonesSchedulerU1_ParseRedundantSizeCloseBeforeOpen pins
// parsing.go:56. `end > start` and `end >= start` can only differ at
// end == start, but `start` is the index of "(" and `end` is the index of ")":
// a single byte position cannot be both characters, so end == start is
// unreachable. With ")" before "(" (end < start), both operators are false and
// the function falls through to the bare-fields path -- here "a ) (" has only
// three fields, so it returns the "0 B" default. Hence parsing.go:56 is
// EQUIVALENT; this locks the close-before-open fallback.
func TestGkDockerFclonesSchedulerU1_ParseRedundantSizeCloseBeforeOpen(t *testing.T) {
	t.Parallel()

	const line = "a ) ("
	if got := ParseRedundantSize(line); got != DefaultSizeStr {
		t.Errorf("ParseRedundantSize(%q) = %q, want %q", line, got, DefaultSizeStr)
	}
}

// TestGkDockerFclonesSchedulerU1_ParseActionSummaryZeroAndNegativeFiles pins
// parsing.go:152. `n >= 0` vs `n > 0` differs only at n == 0, but
// summary.Files defaults to 0, so assigning 0 (original) and leaving 0 (mutant)
// are indistinguishable. A negative count (n < 0, below the boundary) is
// rejected by both operators and likewise leaves Files at 0, while the reclaimed
// size is parsed regardless. Hence parsing.go:152 is EQUIVALENT; this locks the
// zero/negative-file-count contract.
func TestGkDockerFclonesSchedulerU1_ParseActionSummaryZeroAndNegativeFiles(t *testing.T) {
	t.Parallel()

	// n == 0 boundary: Files == 0, reclaimed parsed independently.
	zero := ParseActionSummary("Processed 0 files and reclaimed 4 B space")
	if zero.Files != 0 {
		t.Errorf("ParseActionSummary(zero files).Files = %d, want 0", zero.Files)
	}
	if zero.ReclaimedBytes != 4 {
		t.Errorf("ParseActionSummary(zero files).ReclaimedBytes = %d, want 4", zero.ReclaimedBytes)
	}

	// n < 0 region: negative count rejected, Files stays 0.
	neg := ParseActionSummary("Processed -3 files and reclaimed 4 B space")
	if neg.Files != 0 {
		t.Errorf("ParseActionSummary(negative files).Files = %d, want 0", neg.Files)
	}
}

// TestGkDockerFclonesSchedulerU1_ParseHumanBytesZeroResult pins parsing.go:195.
// `result < 0` vs `result <= 0` differs only at result == 0: the original
// returns `result` (which is 0) and the mutant returns the literal 0 -- the same
// value. A positive input ("2 KB") exercises the result > 0 region identically.
// Hence parsing.go:195 is EQUIVALENT; this locks the zero/positive contract.
func TestGkDockerFclonesSchedulerU1_ParseHumanBytesZeroResult(t *testing.T) {
	t.Parallel()

	if got := ParseHumanBytes("0 B"); got != 0 {
		t.Errorf("ParseHumanBytes(%q) = %d, want 0", "0 B", got)
	}
	if got := ParseHumanBytes("2 KB"); got != 2000 {
		t.Errorf("ParseHumanBytes(%q) = %d, want 2000", "2 KB", got)
	}
}
