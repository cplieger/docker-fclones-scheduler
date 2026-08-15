package main

import (
	"strings"
	"testing"
)

// TestRejectPositionalArgs pins the positional-token gate. A bare token is
// accepted only directly after a flag, where clap reads it as that flag's
// value; anywhere else clap reads it as another input path, which is what
// makes FCLONES_ARGS="--name '*.mp4' '*.mkv'" fail the scan with
// "Can't access '/app/*.mkv'" (fclones issue #509). Quotes are stripped by
// args.Parse before this gate sees the tokens, so a quoted glob and a bare
// one are the same input here.
func TestRejectPositionalArgs(t *testing.T) {
	t.Parallel()

	allowed := []string{
		"",
		"--rf-over 1",
		"--min-size 1M --threads 4",
		"--name '*.mp4*'",               // one pattern, one flag
		"--name '*.mp4' --name '*.mkv'", // the repeat-the-flag fix
		"--name '*.{mp4,mkv}'",          // the one-glob fix
		"--name=*.mkv",                  // clap's =-joined spelling
		"-i --name '*.jpg'",             // boolean flag, then flag + value
		"--depth 3 --follow-links",      // value, then boolean flag
		"--name '-leading-dash*'",       // hyphen-leading tokens are left to clap
		"--transform 'exiv2 -d a $IN'",  // a quoted command is one token
	}
	for _, raw := range allowed {
		if err := rejectPositionalArgs(raw, "TEST_ENV"); err != nil {
			t.Errorf("rejectPositionalArgs(%q, TEST_ENV) = %v, want nil", raw, err)
		}
	}

	rejected := []string{
		"--name '*.mp4*' '*.mkv'",            // issue #509, verbatim
		"--name '*.jpg' '*.png'",             // upstream's own README example
		"--name '*.mp4' '*.mkv' --threads 4", // buried in the middle
		"--name '*.mp4' /media/archive",      // would silently widen the scan
		"/media/archive",                     // a scan path in the wrong env var
		"--threads 4 leftover",               // a stray token after a consumed value
		"-- /media/archive",                  // -- ends option parsing
	}
	for _, raw := range rejected {
		err := rejectPositionalArgs(raw, "TEST_ENV")
		if err == nil {
			t.Errorf("rejectPositionalArgs(%q, TEST_ENV) = nil, want a positional-argument error", raw)
			continue
		}
		if !strings.Contains(err.Error(), "positional argument") {
			t.Errorf("rejectPositionalArgs(%q, TEST_ENV) error = %q, want it to name the positional argument", raw, err)
		}
		if !strings.Contains(err.Error(), "TEST_ENV") {
			t.Errorf("rejectPositionalArgs(%q, TEST_ENV) error = %q, want it to name the env var", raw, err)
		}
	}
}

// TestLoadConfig_positionalArgsRejected verifies the gate holds at loadConfig
// level for both arg vars and in BOTH safety modes: FCLONES_ALLOW_UNSAFE
// relaxes the dangerous-flag guardrails, not the wrapper's scan-path contract.
func TestLoadConfig_positionalArgsRejected(t *testing.T) {
	for _, envVar := range []string{"FCLONES_ARGS", "FCLONES_ACTION_ARGS"} {
		for _, unsafeMode := range []string{"false", "true"} {
			t.Run(envVar+"/allow_unsafe="+unsafeMode, func(t *testing.T) {
				setCleanFclonesEnv(t)
				t.Setenv(envVar, "--name '*.mp4*' '*.mkv'")
				t.Setenv("FCLONES_ALLOW_UNSAFE", unsafeMode)

				_, err := loadConfig()
				if err == nil {
					t.Fatalf("loadConfig(%s=--name '*.mp4*' '*.mkv') = nil error, want the positional-argument rejection", envVar)
				}
				if !strings.Contains(err.Error(), "*.mkv") {
					t.Errorf("loadConfig(%s=--name '*.mp4*' '*.mkv') error = %q, want it to name the offending token", envVar, err)
				}
			})
		}
	}
}
