// Package main is the fclones-wrapper binary: an interval scheduler that runs
// fclones against mounted directories to find, hardlink, or remove duplicate files.
package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/cplieger/docker-fclones-scheduler/internal/args"
	"github.com/cplieger/envx/v2"
	"github.com/cplieger/scheduler/v4"
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

	// Mode is derived from SCAN_INTERVAL (see parseInterval); Interval is
	// consulted only in modeBuiltin.
	Mode runMode
}

// runMode is how the long-running container schedules fclones scans, derived
// from SCAN_INTERVAL.
type runMode int

const (
	// modeBuiltin runs a scan at startup, then every config.Interval.
	modeBuiltin runMode = iota
	// modeExternal idles until SIGTERM; scans are triggered out-of-band via
	// the `scan` subcommand.
	modeExternal
	// modeOnce runs exactly one scan+action then exits.
	modeOnce
)

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

var _ fmt.Stringer = action("")

// String returns the fclones subcommand name for the action.
func (a action) String() string { return string(a) }

const (
	// Configured via volume mounts, not env vars.
	scanDir  = "/scandir"
	cacheDir = "/cache"

	// lockFile serialises scans in-process and cross-process (a manual
	// `docker exec` racing the scheduled run); fclones shares /cache across
	// runs and concurrent scans could corrupt it.
	lockFile = cacheDir + "/.fclones.lock"

	// Bounds each captured subprocess stream so a chatty fclones cannot OOM
	// the container.
	streamCapBytes    = 1 << 20 // 1 MB
	logDetailCapBytes = 64 * 1024

	defaultInterval    = 3 * time.Hour
	defaultScanTimeout = 12 * time.Hour
)

// setupLogger installs a slog text handler that emits canonical logfmt
// (`time=... level=... msg=... k=v`) to stderr.
func setupLogger() {
	raw := envx.String("LOG_LEVEL")
	level, recognized := slogx.ParseLevel(raw, slog.LevelInfo)
	slogx.Setup(slogx.Options{Level: level})
	if !recognized {
		slog.Warn("unrecognized LOG_LEVEL, defaulting to info", "value", raw)
	}
}

// --- Environment ---

// rejectedValue returns the exact string an envx parse failed on, or "" when
// err is not an envx parse error. Carries the value off the error rather
// than re-reading the environment: os.Getenv returns it UNTRIMMED, which
// could mismatch the parse error's value.
func rejectedValue(err error) string {
	if perr, ok := errors.AsType[*envx.ParseError](err); ok {
		return perr.Value
	}
	return ""
}

// loadScanTimeout reads SCAN_TIMEOUT via envx.DurationStrict, defaulting to
// defaultScanTimeout when unset. Zero disables the per-phase deadline. A
// negative value is rejected as a likely typo rather than silently bricking
// the container with an already-expired context.
func loadScanTimeout() (time.Duration, error) {
	scanTimeout, ok, err := envx.DurationStrict("SCAN_TIMEOUT")
	if err != nil {
		raw := rejectedValue(err)
		slog.Error("invalid SCAN_TIMEOUT", "value", raw, logKeyOutcome, "config_error", "error", err)
		return 0, fmt.Errorf("invalid SCAN_TIMEOUT %q: %w", raw, err)
	}
	if !ok {
		return defaultScanTimeout, nil
	}
	if scanTimeout < 0 {
		slog.Error("invalid SCAN_TIMEOUT",
			"value", scanTimeout.String(), logKeyOutcome, "config_error", "error", "must be zero (no timeout) or a positive duration")
		return 0, fmt.Errorf("invalid SCAN_TIMEOUT %q: must be zero (no timeout) or positive", scanTimeout.String())
	}
	return scanTimeout, nil
}

func loadConfig() (config, error) {
	actionStr := cmp.Or(envx.String("FCLONES_ACTION"), string(actionGroup))
	parsedAction, parseErr := parseAction(actionStr)
	if parseErr != nil {
		slog.Error("invalid FCLONES_ACTION", "action", actionStr, logKeyOutcome, "config_error", "error", parseErr)
		return config{}, parseErr
	}

	scanPaths := cmp.Or(envx.String("FCLONES_SCAN_PATHS"), scanDir)
	argsStr := envx.String("FCLONES_ARGS")
	actionArgs := envx.String("FCLONES_ACTION_ARGS")
	if err := validateArgEnvs(argsStr, actionArgs, scanPaths); err != nil {
		return config{}, err
	}

	// A whitespace/quote-only value parses to zero tokens; warn so a missing
	// scan target is obvious at startup rather than a cryptic exec error.
	if parsed, perr := args.Parse(scanPaths); perr == nil && len(parsed) == 0 {
		slog.Warn("FCLONES_SCAN_PATHS resolved to no scan targets; fclones will have no path to scan",
			"value", scanPaths)
	}

	scanTimeout, timeoutErr := loadScanTimeout()
	if timeoutErr != nil {
		return config{}, timeoutErr
	}

	// SCAN_INTERVAL sets the built-in cadence; see parseInterval for sentinels.
	interval, mode := parseInterval(os.Getenv("SCAN_INTERVAL"))

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

// parseInterval interprets SCAN_INTERVAL into the built-in scan interval and
// run mode: "off"/"disabled" select external mode, zero selects run-once,
// a positive duration sets the built-in cadence, and an empty, unparseable,
// or negative value falls back to defaultInterval in built-in mode.
func parseInterval(raw string) (interval time.Duration, mode runMode) {
	s := scheduler.ParseInterval(raw, defaultInterval,
		scheduler.WithZeroAsOnce(true), scheduler.WithName("SCAN_INTERVAL"))
	switch s.Mode {
	case scheduler.ModeExternal:
		return s.Interval, modeExternal
	case scheduler.ModeOnce:
		return s.Interval, modeOnce
	default:
		return s.Interval, modeBuiltin
	}
}

// Dangerous fclones flags that execute arbitrary commands or modify files in
// place.
const (
	flagTransform = "--transform"
	flagInPlace   = "--in-place"
	flagNoCopy    = "--no-copy"
)

// Dangerous fclones flags that execute arbitrary commands or modify files in
// place. Audited against fclones v0.35.0; re-audit on FCLONES_VERSION bump
// (the docker-fclones-scheduler steering doc names the exact-match caveats).
var dangerousFlags = []string{flagTransform, flagInPlace, flagNoCopy}

// wrapperOwnedFlags are fclones flags the wrapper itself appends
// (buildScanArgs): --cache shares the hash cache, --format is the JSON
// report contract DecodeReport is built against. Rejected in FCLONES_ARGS
// unconditionally (a contract, not a safety guardrail). -f is handled
// separately below since clap accepts -f json/-f=json/-fjson.
var wrapperOwnedFlags = []string{"--format", "--cache"}

// rejectWrapperOwnedArgs blocks wrapper-owned fclones flags (see
// wrapperOwnedFlags) in FCLONES_ARGS so a user config cannot silently break
// the report contract or redirect the shared cache.
func rejectWrapperOwnedArgs(raw string) error {
	parsed, err := parseArgString(raw, "FCLONES_ARGS")
	if err != nil {
		return err
	}
	for _, arg := range parsed {
		lower := strings.ToLower(arg)
		owned := strings.HasPrefix(lower, "-f") && !strings.HasPrefix(lower, "--")
		for _, flag := range wrapperOwnedFlags {
			if lower == flag || strings.HasPrefix(lower, flag+"=") {
				owned = true
				break
			}
		}
		if owned {
			slog.Error("wrapper-owned flag not allowed",
				"flag", arg, "env", "FCLONES_ARGS", logKeyOutcome, "config_error")
			return fmt.Errorf("wrapper-owned flag %q not allowed in FCLONES_ARGS (the wrapper sets --cache and -f json itself)", arg)
		}
	}
	return nil
}

// validateArgEnvs runs every startup gate over the three env vars that carry
// fclones arguments. Only the dangerous-flag denylist is relaxed by
// ALLOW_UNSAFE_ARGS; the wrapper-owned and positional-token checks are
// contracts, not safety guardrails, so they hold in both modes.
func validateArgEnvs(argsStr, actionArgs, scanPaths string) error {
	// ALLOW_UNSAFE_ARGS accepts only the exact spelling "true"
	// (case-insensitive), not envx.Bool's tolerant 1/yes/on set, since it
	// disables command-injection guardrails.
	unsafeAllowed := strings.EqualFold(cmp.Or(envx.String("ALLOW_UNSAFE_ARGS"), "false"), "true")
	if unsafeAllowed {
		slog.Warn("unsafe flags allowed, command injection guardrails disabled",
			"env", "ALLOW_UNSAFE_ARGS")
	}
	for _, p := range []struct{ raw, env string }{
		{argsStr, "FCLONES_ARGS"},
		{actionArgs, "FCLONES_ACTION_ARGS"},
		{scanPaths, "FCLONES_SCAN_PATHS"},
	} {
		if unsafeAllowed {
			if _, err := parseArgString(p.raw, p.env); err != nil {
				return err
			}
			continue
		}
		if err := rejectDangerousArgs(p.raw, p.env); err != nil {
			return err
		}
	}

	if err := rejectWrapperOwnedArgs(argsStr); err != nil {
		return err
	}

	for _, p := range []struct{ raw, env string }{
		{argsStr, "FCLONES_ARGS"},
		{actionArgs, "FCLONES_ACTION_ARGS"},
	} {
		if err := rejectPositionalArgs(p.raw, p.env); err != nil {
			return err
		}
	}
	return nil
}

// rejectPositionalArgs blocks bare (non-flag) tokens in a flags-only env var.
// Every fclones repeatable option takes exactly one value per occurrence, so
// a second bare token is read as another scan path rather than another
// pattern (issue #509; see the docker-fclones-scheduler steering doc "fclones
// flag arity" for the full mechanism and the upstream README example it
// breaks). A stray token naming a real directory silently widens the scan,
// and link/remove/dedupe then mutate files outside FCLONES_SCAN_PATHS.
//
// A bare token is accepted only directly after a flag (its value). "--" is
// rejected too: it ends clap's option parsing, so every token after it
// becomes a scan path.
func rejectPositionalArgs(raw, envVar string) error {
	parsed, err := parseArgString(raw, envVar)
	if err != nil {
		return err
	}
	prevIsFlag := false
	for _, arg := range parsed {
		isFlag := strings.HasPrefix(arg, "-") && arg != "--"
		if !isFlag && !prevIsFlag {
			slog.Error("positional argument not allowed",
				"arg", arg, "env", envVar, logKeyOutcome, "config_error")
			return fmt.Errorf("positional argument %q not allowed in %s (fclones reads it as an extra scan path; repeat the flag per value, e.g. --name '*.mp4' --name '*.mkv', or set FCLONES_SCAN_PATHS)", arg, envVar)
		}
		prevIsFlag = isFlag
	}
	return nil
}

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
// or a timeout error if timeout elapses (or ctx is cancelled) first. probe is
// a parameter so tests can inject a blocking probe to exercise the timeout
// branch deterministically.
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
