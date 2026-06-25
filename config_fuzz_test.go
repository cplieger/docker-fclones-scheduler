package main

import (
	"strings"
	"testing"

	"github.com/cplieger/fclones-wrapper/internal/args"
)

func FuzzRejectDangerousArgs(f *testing.F) {
	f.Add("--min-size 1M")
	f.Add("--command rm")
	f.Add("--transform 's/foo/bar/'")
	f.Add("--COMMAND upper")
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
		wantReject := false
		for _, arg := range parsed {
			lower := strings.ToLower(arg)
			for _, flag := range []string{"--command", "--transform", "--in-place", "--no-copy"} {
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
