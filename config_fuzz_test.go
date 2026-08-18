package main

import (
	"strings"
	"testing"

	"github.com/cplieger/docker-fclones-scheduler/internal/args"
)

func FuzzRejectDangerousArgs(f *testing.F) {
	f.Add("--min-size 1M")
	f.Add("--transform 's/foo/bar/'")
	f.Add("--TRANSFORM upper")
	f.Add("--transformer x")
	f.Add("--in-place")
	f.Add("--no-copy")
	f.Add("")
	f.Fuzz(func(t *testing.T, input string) {
		err := rejectDangerousArgs(input, "TEST_ENV")
		parsed, parseErr := args.Parse(input)
		if parseErr != nil {
			if err == nil {
				t.Fatalf("rejectDangerousArgs(%q) = nil, want error for unparseable input", input)
			}
			return
		}
		// The oracle re-derives the MATCHING rule (exact match or a "flag="
		// prefix, case-folded) over the production list rather than a second
		// copy of it: a hardcoded copy is what let a phantom --command entry
		// live in both places. The list's CONTENTS are pinned by
		// TestRejectDangerousArgs instead.
		wantReject := false
		for _, arg := range parsed {
			lower := strings.ToLower(arg)
			for _, flag := range dangerousFlags {
				if lower == flag || strings.HasPrefix(lower, flag+"=") {
					wantReject = true
				}
			}
		}
		switch {
		case wantReject && err == nil:
			t.Fatalf("dangerous flag not rejected in %v", parsed)
		case !wantReject && err != nil:
			t.Fatalf("clean input rejected: %v", parsed)
		}
	})
}

func FuzzParseAction(f *testing.F) {
	f.Add("group")
	f.Add("remove")
	f.Add("link")
	f.Add("dedupe")
	f.Add("invalid")
	f.Add("")
	f.Add("GROUP")
	f.Fuzz(func(t *testing.T, input string) {
		act, err := parseAction(input)
		validSet := map[action]bool{actionGroup: true, actionRemove: true, actionLink: true, actionDedupe: true}
		if err == nil {
			// Must be a valid action
			if !validSet[act] {
				t.Fatalf("parseAction(%q) returned unknown action %q", input, act)
			}
			// String representation must match input
			if act.String() != input {
				t.Fatalf("action.String() = %q, want %q", act.String(), input)
			}
		} else {
			// Error must mention "invalid action"
			if !strings.Contains(err.Error(), "invalid action") {
				t.Fatalf("unexpected error format: %v", err)
			}
		}
	})
}

func FuzzParseInterval(f *testing.F) {
	f.Add("3h")
	f.Add("90m")
	f.Add("off")
	f.Add("disabled")
	f.Add("0")
	f.Add("0s")
	f.Add("-1h")
	f.Add("not-a-duration")
	f.Add("")
	f.Add("   ")
	f.Fuzz(func(t *testing.T, input string) {
		interval, mode := parseInterval(input)

		// The returned interval must always be positive: the daemon ticker passes it
		// to time.NewTicker (main.go), which panics on a non-positive duration.
		// parseInterval is the sole gate protecting that call from an arbitrary
		// SCAN_INTERVAL env value.
		if interval <= 0 {
			t.Fatalf("parseInterval(%q) interval = %s, want > 0 (time.NewTicker panics on a non-positive duration)", input, interval)
		}
		// The mode must be one of the three defined run modes; a fourth value
		// would panic run()'s and runMode.String()'s exhaustive switches.
		switch mode {
		case modeBuiltin, modeExternal, modeOnce:
		default:
			t.Fatalf("parseInterval(%q) mode = %d, want built-in, external, or once", input, int(mode))
		}
	})
}
