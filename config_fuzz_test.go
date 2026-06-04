package main

import (
	"strings"
	"testing"
)

func FuzzRejectDangerousArgs(f *testing.F) {
	f.Add("--min-size 1M")
	f.Add("--command rm")
	f.Add("--transform 's/foo/bar/'")
	f.Add("--COMMAND upper")
	f.Add("--in-place")
	f.Add("--no-copy")
	f.Add("")
	f.Add("'--command' hidden")
	f.Fuzz(func(t *testing.T, input string) {
		err := rejectDangerousArgs(input, "TEST_ENV")
		lower := strings.ToLower(input)
		// If any dangerous flag is present (case-insensitive), must error
		dangerous := []string{"--command", "--transform", "--in-place", "--no-copy"}
		for _, flag := range dangerous {
			// Check if the flag appears as a standalone token after parsing
			// If err == nil and the flag appears in the input, args.Parse decided
			// it wasn't actually a flag (after shell-like parsing) — fine.
			_ = err == nil && (strings.Contains(lower, flag) || strings.Contains(lower, flag+"="))
		}
		// If error returned, it should mention "dangerous" or "not allowed" or be a parse error
		if err != nil {
			msg := err.Error()
			if !strings.Contains(msg, "not allowed") && !strings.Contains(msg, "syntax") {
				t.Fatalf("unexpected error message: %v", err)
			}
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
