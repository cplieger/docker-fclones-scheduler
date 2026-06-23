// Package main is the fclones-wrapper binary: an interval scheduler that runs
// fclones against mounted directories to find, hardlink, or remove duplicate files.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cplieger/fclones-wrapper/internal/args"
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

	// ScheduleEnabled reports whether the built-in interval scheduler runs.
	// When false (FCLONES_INTERVAL=off/disabled/0), the container idles and
	// scans are triggered out-of-band via the `scan` subcommand (e.g. an
	// external scheduler such as Ofelia running `docker exec`). Interval is
	// not consulted in that mode.
	ScheduleEnabled bool
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
	switch action(s) {
	case actionGroup, actionRemove, actionLink, actionDedupe:
		return action(s), nil
	default:
		names := make([]string, len(validActions))
		for i, a := range validActions {
			names[i] = string(a)
		}
		return "", fmt.Errorf("invalid action %q (allowed: %s)", s, strings.Join(names, ", "))
	}
}

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

	// Memory caps. stderr is bounded so a chatty fclones cannot OOM the
	// container; the output report read is bounded against very large
	// duplicate reports; the duplicate-log detail is bounded against Loki's
	// line-length limit.
	stderrCapBytes    = 1 << 20  // 1 MB
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
	levelStr := strings.ToLower(strings.TrimSpace(getEnv("FCLONES_LOG_LEVEL", "info")))
	level := slog.LevelInfo
	switch levelStr {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: level,
	})))
}

// --- Environment ---

func loadConfig() (config, error) {
	actionStr := getEnv("FCLONES_ACTION", string(actionGroup))
	parsedAction, parseErr := parseAction(actionStr)
	if parseErr != nil {
		allowed := make([]string, len(validActions))
		for i, a := range validActions {
			allowed[i] = string(a)
		}
		slog.Error("invalid FCLONES_ACTION", "action", actionStr, "allowed", strings.Join(allowed, ", "), "error", parseErr)
		return config{}, parseErr
	}

	scanPaths := getEnv("FCLONES_SCAN_PATHS", scanDir)
	argsStr := getEnv("FCLONES_ARGS", "")
	actionArgs := getEnv("FCLONES_ACTION_ARGS", "")
	if strings.EqualFold(getEnv("FCLONES_ALLOW_UNSAFE", "false"), "true") {
		slog.Warn("unsafe flags allowed, command injection guardrails disabled",
			"env", "FCLONES_ALLOW_UNSAFE")
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

	timeoutStr := getEnv("FCLONES_SCAN_TIMEOUT", "12h")
	scanTimeout, err := time.ParseDuration(timeoutStr)
	if err != nil {
		slog.Error("invalid FCLONES_SCAN_TIMEOUT", "value", timeoutStr, "error", err)
		return config{}, fmt.Errorf("invalid FCLONES_SCAN_TIMEOUT %q: %w", timeoutStr, err)
	}

	// FCLONES_INTERVAL is a Go duration (e.g., "1h", "30m", "12h") that
	// sets the built-in scan cadence. The sentinels "off", "disabled", or
	// any zero duration ("0", "0s") disable the built-in scheduler; the
	// container then idles and scans are triggered via the `scan`
	// subcommand by an external scheduler. On any other parse failure the
	// loader falls back to defaultInterval and logs a warning rather than
	// refusing to start, keeping the container scanning on a reasonable
	// cadence even with a malformed env block.
	interval := defaultInterval
	scheduleEnabled := true
	if raw := strings.TrimSpace(os.Getenv("FCLONES_INTERVAL")); raw != "" {
		switch strings.ToLower(raw) {
		case "off", "disabled":
			scheduleEnabled = false
		default:
			if d, perr := time.ParseDuration(raw); perr == nil {
				if d > 0 {
					interval = d
				} else {
					// Zero duration ("0", "0s") means disable scheduling.
					scheduleEnabled = false
				}
			} else {
				slog.Warn("cannot parse FCLONES_INTERVAL, using default",
					"value", raw, "default", defaultInterval)
			}
		}
	}

	return config{
		Interval:        interval,
		ScheduleEnabled: scheduleEnabled,
		ScanPath:        scanPaths,
		Args:            argsStr,
		Action:          parsedAction,
		ActionArgs:      actionArgs,
		PhaseTimeout:    scanTimeout,
	}, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Dangerous fclones flags that execute arbitrary commands.
const (
	flagCommand   = "--command"
	flagTransform = "--transform"
	flagInPlace   = "--in-place"
	flagNoCopy    = "--no-copy"
)

var dangerousFlags = []string{flagCommand, flagTransform, flagInPlace, flagNoCopy}

// rejectDangerousArgs blocks fclones flags that could execute arbitrary commands.
func rejectDangerousArgs(raw, envVar string) error {
	parsed, err := args.Parse(raw)
	if err != nil {
		slog.Error("invalid argument syntax", "env", envVar, "error", err)
		return fmt.Errorf("invalid argument syntax in %s: %w", envVar, err)
	}
	for _, arg := range parsed {
		lower := strings.ToLower(arg)
		for _, flag := range dangerousFlags {
			if lower == flag || strings.HasPrefix(lower, flag+"=") {
				slog.Error("dangerous flag not allowed", "flag", flag, "env", envVar)
				return fmt.Errorf("dangerous flag %s not allowed in %s", flag, envVar)
			}
		}
	}
	return nil
}

// verifyCacheDir ensures the cache directory exists and is writable.
func verifyCacheDir(ctx context.Context) error {
	if err := verifyDir(ctx, cacheDir, 10*time.Second); err != nil {
		return fmt.Errorf("cache directory verification failed (path=%s, uid=%d): %w",
			cacheDir, os.Getuid(), err)
	}
	slog.Debug("cache directory verified", "path", cacheDir)
	return nil
}

// verifyDir creates dir if missing and confirms a file can be written
// inside it.
func verifyDir(ctx context.Context, dir string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("verification timed out after %s", timeout)
	}
	testFile := filepath.Join(dir, ".write_test")
	f, err := os.Create(testFile)
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}
	f.Close()
	os.Remove(testFile)
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("verification timed out after %s", timeout)
	}
	return nil
}
