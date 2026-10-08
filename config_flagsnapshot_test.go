package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/docker-fclones-scheduler/internal/fclonesflags"
)

// TestArgumentGates_nameRealFclonesFlags pins every flag the argument gates
// refuse to an option the pinned fclones has. A denylist entry for a flag
// fclones lacks blocks nothing while the docs promise a refusal; an upstream
// removal fails here once flags.txt is regenerated.
func TestArgumentGates_nameRealFclonesFlags(t *testing.T) {
	t.Parallel()
	known := map[string]bool{}
	for _, entry := range fclonesflags.Snapshot() {
		_, flag, _ := strings.Cut(entry, " ")
		known[flag] = true
	}
	if len(known) == 0 {
		t.Fatal("fclonesflags.Snapshot() is empty")
	}
	// rejectWrapperOwnedArgs refuses -f, the short form of --format, on its own.
	for _, flag := range slices.Concat(dangerousFlags, wrapperOwnedFlags, []string{"-f"}) {
		if !known[flag] {
			t.Errorf("argument gate refuses %s, which internal/fclonesflags/flags.txt does not list", flag)
		}
	}
}
