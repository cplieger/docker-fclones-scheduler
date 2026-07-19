package main

import (
	"strings"
	"testing"
)

// TestRejectWrapperOwnedArgs pins the wrapper-owned flag gate: --cache and
// the report-format flag (--format long form; -f short form in clap's
// separate, =-joined, and attached spellings) are startup errors in
// FCLONES_ARGS, while ordinary flags pass. Comparison is case-folded,
// mirroring rejectDangerousArgs' defense-in-depth posture.
func TestRejectWrapperOwnedArgs(t *testing.T) {
	t.Parallel()

	allowed := []string{
		"",
		"--rf-over 1",
		"--min-size 1M --threads 4",
		"--follow-links",  // long flag starting with an f is not the -f short flag
		"--name '*.file'", // value containing the letter f is fine
	}
	for _, raw := range allowed {
		if err := rejectWrapperOwnedArgs(raw); err != nil {
			t.Errorf("rejectWrapperOwnedArgs(%q) = %v, want nil", raw, err)
		}
	}

	rejected := []string{
		"-f json",
		"-f=json",
		"-fjson", // clap's attached short-flag value spelling
		"-F JSON",
		"--format json",
		"--format=json",
		"--FORMAT json",
		"--cache",
		"--cache=off",
		"--rf-over 1 --cache", // rejected regardless of position
	}
	for _, raw := range rejected {
		err := rejectWrapperOwnedArgs(raw)
		if err == nil {
			t.Errorf("rejectWrapperOwnedArgs(%q) = nil, want a wrapper-owned flag error", raw)
			continue
		}
		if !strings.Contains(err.Error(), "wrapper-owned flag") {
			t.Errorf("rejectWrapperOwnedArgs(%q) error = %q, want it to name the wrapper-owned flag", raw, err)
		}
	}
}

// TestLoadConfig_wrapperOwnedFlagsRejected verifies the gate holds at
// loadConfig level in BOTH safety modes: FCLONES_ALLOW_UNSAFE relaxes the
// dangerous-flag guardrails, not the wrapper's own report/cache contract.
func TestLoadConfig_wrapperOwnedFlagsRejected(t *testing.T) {
	for _, unsafeMode := range []string{"false", "true"} {
		t.Run("allow_unsafe="+unsafeMode, func(t *testing.T) {
			setCleanFclonesEnv(t)
			t.Setenv("FCLONES_ARGS", "--format json")
			t.Setenv("FCLONES_ALLOW_UNSAFE", unsafeMode)

			if _, err := loadConfig(); err == nil {
				t.Fatal("loadConfig(FCLONES_ARGS=--format json) = nil error, want the wrapper-owned rejection")
			}
		})
	}
}
