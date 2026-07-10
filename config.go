// Package main is the fclones-wrapper binary: an interval scheduler that runs
// fclones against mounted directories to find, hardlink, or remove duplicate files.
package main

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/cplieger/fclones-wrapper/internal/args"
	"github.com/cplieger/scheduler"
	"github.com/cplieger/slogx"
)

// --- Configuration ---

// config holds all user-configurable settings loaded from environment variables.
type config struct {
	ScanPath     string
	Args         string
	Action       action
	ActionArgs   string
	Interval     time.Duration
	PhaseTimeout time.Duration

	// Mode selects how the daemon schedules scans, derived from
	// FCLONES_INTERVAL (see parseInterval). Interval is consulted only in
	// modeBuiltin.
	Mode runMode
}

// runMode is how the long-running container schedules fclones scans, derived
// from FCLONES_INTERVAL.
type runMode int

const (
	// modeBuiltin runs a scan at startup, then every config.Interval. Selected
	// by a positive FCLONES_INTERVAL, or by the default cadence when the value
	// is empty, unparseable, or negative.
	modeBuiltin runMode = iota
	// modeExternal idles until SIGTERM; scans are triggered out-of-band via the
	// `scan` subcommand (e.g. Ofelia `docker exec`). Selected by
	// FCLONES_INTERVAL=off/disabled.
	modeExternal
	// modeOnce runs exactly one scan+action then exits. Selected by a zero
	// FCLONES_INTERVAL (0/0s); the process exits non-zero if that scan fails.
	modeOnce
)

// Compile-time assertion: runMode implements fmt.Stringer (mirrors the action
// and phaseOutcome assertions).
var _ fmt.Stringer = runMode(0)

// String returns the human-readable mode name for log lines.
func (m runMode) String() string {
	switch m {
	case modeBuiltin:
		return "built-in"
	case modeExternal:
		return "external"
	case modeOnce:
		return "once"
	default:
		panic(fmt.Sprintf("unhandled runMode: %d", int(m)))
	}
}

// action represents a validated fclones subcommand.
type action string

const (
	actionGroup  action = "group"
	actionRemove action = "remove"
	actionLink   action = "link"
	actionDedupe action = "dedupe"
)

// validActions lists all accepted action values for error messages.
var validActions = []action{actionGroup, actionRemove, actionLink, actionDedupe}

// parseAction validates a raw string and returns the corresponding action.
// Returns an error if the string is not a recognised fclones subcommand.
func parseAction(s string) (action, error) {
	candidate := action(s)
	if slices.Contains(validActions, candidate) {
		return candidate, nil
	}
	names := make([]string, len(validActions))
	for i, a := range validActions {
		names[i] = string(a)
	}
	return "", fmt.Errorf("invalid action %q (allowed: %s)", s, strings.Join(names, ", "))
}

// Compile-time assertion: action implements fmt.Stringer (mirrors the
// phaseOutcome assertion in outcome.go).
var _ fmt.Stringer = action("")

// String returns the fclones subcommand name for the action.
func (a action) String() string { return string(a) }

const (
	// Fixed container paths — configured via volume mounts, not env vars.
	scanDir  = "/scandir"
	cacheDir = "/cache"

	// lockFile guards against overlapping scans. flock(2) on this file
	// serialises runs both in-process (the built-in ticker racing the
	// startup scan) and cross-process (an external `scan` invocation racing
	// a manual `docker exec` or the scheduled one). fclones shares the
	// /cache directory across runs, so concurrent scans could corrupt it.
	lockFile = cacheDir + "/.fclones.lock"

	// Memory caps. Each captured subprocess stream (fclones' stderr, and the
	// action phase's stdout) is bounded so a chatty fclones cannot OOM the
	// container; the output report read is bounded against very large
	// duplicate reports; the duplicate-log detail is bounded against Loki's
	// line-length limit.
	streamCapBytes    = 1 << 20  // 1 MB
	outputCapBytes    = 50 << 20 // 50 MB
	logDetailCapBytes = 64 * 1024

	// defaultInterval is the fallback scan cadence when FCLONES_INTERVAL
	// is unset or unparseable. Three hours matches the historical
	// default and is conservative enough to avoid disk thrash on large
	// libraries while still catching new duplicates within a workday.
	defaultInterval = 3 * time.Hour
)

// setupLogger installs a slog text handler that emits canonical logfmt
// (`time=... level=... msg=... k=v`) to stderr.
func setupLogger() {
	raw := strings.TrimSpace(getEnv("FCLONES_LOG_LEVEL", "info"))
	level, recognized := slogx.ParseLevel(raw, slog.LevelInfo)
	slogx.Setup(slogx.Options{Level: level})
	if !recognized {
		slog.Warn("unrecognized FCLONES_LOG_LEVEL, defaulting to info", "value", raw)
	}
}

// --- Environment ---

func loadConfig() (config, error) {
	actionStr := getEnv("FCLONES_ACTION", string(actionGroup))
	parsedAction, parseErr := parseAction(actionStr)
	if parseErr != nil {
		slog.Error("invalid FCLONES_ACTION", "action", actionStr, logKeyOutcome, "config_error", "error", parseErr)
		return config{}, parseErr
	}

	scanPaths := getEnv("FCLONES_SCAN_PATHS", scanDir)
	argsStr := getEnv("FCLONES_ARGS", "")
	actionArgs := getEnv("FCLONES_ACTION_ARGS", "")
	if strings.EqualFold(getEnv("FCLONES_ALLOW_UNSAFE", "false"), "true") {
		slog.Warn("unsafe flags allowed, command injection guardrails disabled",
			"env", "FCLONES_ALLOW_UNSAFE")
		// Unsafe mode skips the dangerous-flag check but must still validate
		// argument syntax so a quoting typo fails fast at startup.
		for _, p := range []struct{ raw, env string }{
			{argsStr, "FCLONES_ARGS"},
			{actionArgs, "FCLONES_ACTION_ARGS"},
			{scanPaths, "FCLONES_SCAN_PATHS"},
		} {
			if _, perr := parseArgString(p.raw, p.env); perr != nil {
				return config{}, perr
			}
		}
	} else {
		if err := rejectDangerousArgs(argsStr, "FCLONES_ARGS"); err != nil {
			return config{}, err
		}
		if err := rejectDangerousArgs(actionArgs, "FCLONES_ACTION_ARGS"); err != nil {
			return config{}, err
		}
		if err := rejectDangerousArgs(scanPaths, "FCLONES_SCAN_PATHS"); err != nil {
			return config{}, err
		}
	}

	// A whitespace- or quote-only FCLONES_SCAN_PATHS parses to zero tokens, so fclones
	// would run with no scan target. Warn at startup so the cause is obvious instead of
	// surfacing later as a cryptic exec error.
	if parsed, perr := args.Parse(scanPaths); perr == nil && len(parsed) == 0 {
		slog.Warn("FCLONES_SCAN_PATHS resolved to no scan targets; fclones will have no path to scan",
			"value", scanPaths)
	}

	timeoutStr := getEnv("FCLONES_SCAN_TIMEOUT", "12h")
	scanTimeout, err := time.ParseDuration(timeoutStr)
	if err != nil {
		slog.Error("invalid FCLONES_SCAN_TIMEOUT", "value", timeoutStr, logKeyOutcome, "config_error", "error", err)
		return config{}, fmt.Errorf("invalid FCLONES_SCAN_TIMEOUT %q: %w", timeoutStr, err)
	}
	// Zero (FCLONES_SCAN_TIMEOUT=0/0s) disables the per-phase deadline: the
	// phase then runs under the parent context (see phaseContext) with no time
	// limit. A negative duration is almost certainly a typo (e.g. "-1h" for
	// "1h") and would otherwise build an already-expired context that fails
	// every scan, so reject it rather than silently bricking the container.
	if scanTimeout < 0 {
		slog.Error("invalid FCLONES_SCAN_TIMEOUT",
			"value", timeoutStr, logKeyOutcome, "config_error", "error", "must be zero (no timeout) or a positive duration")
		return config{}, fmt.Errorf("invalid FCLONES_SCAN_TIMEOUT %q: must be zero (no timeout) or positive", timeoutStr)
	}

	// FCLONES_INTERVAL sets the built-in scan cadence; see parseInterval for the
	// sentinel ("off"/"disabled"/zero) and fallback rules.
	interval, mode := parseInterval(os.Getenv("FCLONES_INTERVAL"))

	return config{
		Interval:     interval,
		Mode:         mode,
		ScanPath:     scanPaths,
		Args:         argsStr,
		Action:       parsedAction,
		ActionArgs:   actionArgs,
		PhaseTimeout: scanTimeout,
	}, nil
}

// parseInterval interprets FCLONES_INTERVAL into the built-in scan interval and
// the run mode. It delegates to scheduler.ParseInterval with WithZeroAsOnce, so:
// "off"/"disabled" select external (idle) mode; a zero duration ("0"/"0s")
// selects run-once mode (one scan, then exit); a positive duration sets the
// built-in cadence; and an empty, unparseable, or negative value falls back to
// defaultInterval in built-in mode so the container keeps scanning (a negative
// value is a likely typo and is warned about). The library's Schedule.Mode is
// mapped onto the app's runMode.
func parseInterval(raw string) (interval time.Duration, mode runMode) {
	s := scheduler.ParseInterval(raw, defaultInterval,
		scheduler.WithZeroAsOnce(), scheduler.WithName("FCLONES_INTERVAL"))
	switch s.Mode {
	case scheduler.ModeExternal:
		return s.Interval, modeExternal
	case scheduler.ModeOnce:
		return s.Interval, modeOnce
	default:
		return s.Interval, modeBuiltin
	}
}

func getEnv(key, fallback string) string {
	return cmp.Or(os.Getenv(key), fallback)
}

// Dangerous fclones flags that execute arbitrary commands.
const (
	flagCommand   = "--command"
	flagTransform = "--transform"
	flagInPlace   = "--in-place"
	flagNoCopy    = "--no-copy"
)

// Audited against fclones v0.35.0; re-audit on FCLONES_VERSION bump.
// The exact-match denylist is complete only while upstream fclones (clap v4)
// (1) does NOT enable infer_long_args (else abbreviations like --trans expand
// to --transform and bypass this list) and (2) gives no short alias to a
// command-executing / in-place flag. Re-verify both in fclones config.rs/main.rs
// on a version bump, in addition to diffing `--help` for new exec/in-place flags.
var dangerousFlags = []string{flagCommand, flagTransform, flagInPlace, flagNoCopy}

// parseArgString splits a shell-style env value into tokens, logging and
// wrapping any syntax error with the env var name so every caller surfaces
// an identical "invalid argument syntax in <ENV>" message.
func parseArgString(raw, envVar string) ([]string, error) {
	parsed, err := args.Parse(raw)
	if err != nil {
		slog.Error("invalid argument syntax", "env", envVar, logKeyOutcome, "config_error", "error", err)
		return nil, fmt.Errorf("invalid argument syntax in %s: %w", envVar, err)
	}
	return parsed, nil
}

// rejectDangerousArgs blocks fclones flags that could execute arbitrary commands.
func rejectDangerousArgs(raw, envVar string) error {
	parsed, err := parseArgString(raw, envVar)
	if err != nil {
		return err
	}
	for _, arg := range parsed {
		lower := strings.ToLower(arg)
		for _, flag := range dangerousFlags {
			if lower == flag || strings.HasPrefix(lower, flag+"=") {
				slog.Error("dangerous flag not allowed", "flag", flag, "env", envVar, logKeyOutcome, "config_error")
				return fmt.Errorf("dangerous flag %s not allowed in %s", flag, envVar)
			}
		}
	}
	return nil
}

// verifyCacheDir ensures the cache directory exists and is writable.
func verifyCacheDir(ctx context.Context) error {
	if err := verifyDir(ctx, cacheDir, 10*time.Second); err != nil {
		return fmt.Errorf("verify cache dir: %w", err)
	}
	slog.Debug("cache directory verified", "path", cacheDir)
	return nil
}

// verifyDir creates dir if missing and confirms a file can be written
// inside it, bounded by timeout so a hung filesystem cannot block startup.
func verifyDir(ctx context.Context, dir string, timeout time.Duration) error {
	return verifyDirWithProbe(ctx, dir, timeout, writeProbe)
}

// writeProbe creates dir if missing and confirms a file can be written inside it.
func writeProbe(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	testFile := filepath.Join(dir, ".write_test")
	f, err := os.Create(testFile)
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}
	_ = f.Close()
	_ = os.Remove(testFile)
	return nil
}

// verifyDirWithProbe runs probe(dir) in a goroutine and returns its result,
// or a timeout error if timeout elapses (or ctx is cancelled) first. The
// probe is a parameter so tests can inject a blocking probe and exercise the
// timeout branch deterministically; production passes writeProbe with a
// non-cancellable context, so the select only races "probe done" vs timeout.
func verifyDirWithProbe(ctx context.Context, dir string, timeout time.Duration, probe func(string) error) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- probe(dir) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return fmt.Errorf("verification timed out after %s", timeout)
	}
}
