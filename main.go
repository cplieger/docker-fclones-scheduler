package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/robfig/cron/v3"
)

// --- Configuration ---

// config holds all user-configurable settings loaded from environment variables.
type config struct {
	Schedule   string
	ScanPath   string
	Args       string
	Action     string
	ActionArgs string
}

// allowedActions restricts FCLONES_ACTION to official fclones subcommands
// to prevent command injection via environment variables.
// See: https://github.com/pkolaczk/fclones#usage
var allowedActions = map[string]bool{
	actionGroup: true,
	"remove":    true,
	"link":      true,
	"dedupe":    true,
}

const actionGroup = "group"

// healthFile is touched on startup and after successful scans,
// removed on scan failure. The "health" subcommand checks its existence
// for Docker healthchecks without requiring an HTTP server or open port.
const healthFile = "/tmp/.healthy"

var (
	mu         sync.Mutex
	currentJob *exec.Cmd
)

const (
	// Fixed container paths — configured via volume mounts, not env vars.
	scanDir  = "/scandir"
	cacheDir = "/cache"
)

// --- Main ---

func main() {
	// CLI health probe for Docker healthcheck (distroless has no curl/wget).
	// Checks for a marker file instead of making an HTTP request — no port needed.
	if len(os.Args) > 1 && os.Args[1] == "health" {
		if _, err := os.Stat(healthFile); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}

	cfg := loadConfig()
	verifyCacheDir()

	c := cron.New()

	// Validate schedule before setting up signal handling — exit immediately on bad config.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	_, err := c.AddFunc(cfg.Schedule, func() {
		runFclonesJob(ctx, &cfg)
	})
	if err != nil {
		stop()
		slog.Error("invalid cron schedule", "schedule", cfg.Schedule, "error", err)
		os.Exit(1)
	}

	// Mark healthy on startup, remove on exit.
	setHealthy(true)
	defer func() {
		setHealthy(false)
		stop()
	}()

	c.Start()
	slog.Info("container started",
		"uid", os.Getuid(), "schedule", cfg.Schedule,
		"target", cfg.ScanPath, "action", cfg.Action)

	slog.Info("triggering startup scan")
	go runFclonesJob(ctx, &cfg)

	<-ctx.Done()
	slog.Info("shutting down", "cause", context.Cause(ctx))

	// Wait for any running cron job to finish before exiting.
	<-c.Stop().Done()
}

// --- Health ---

// setHealthy creates or removes the health marker file.
func setHealthy(ok bool) {
	if ok {
		if f, err := os.Create(healthFile); err == nil {
			f.Close()
		}
	} else {
		os.Remove(healthFile)
	}
}

// --- Environment ---

func loadConfig() config {
	action := getEnv("FCLONES_ACTION", actionGroup)
	if !allowedActions[action] {
		slog.Error("invalid FCLONES_ACTION", "action", action,
			"allowed", "group, remove, link, dedupe")
		os.Exit(1)
	}

	args := getEnv("FCLONES_ARGS", "")
	actionArgs := getEnv("FCLONES_ACTION_ARGS", "")
	rejectDangerousArgs(args, "FCLONES_ARGS")
	rejectDangerousArgs(actionArgs, "FCLONES_ACTION_ARGS")

	return config{
		Schedule:   getEnv("FCLONES_SCHEDULE", "0 */3 * * *"),
		ScanPath:   getEnv("FCLONES_SCAN_PATHS", scanDir),
		Args:       args,
		Action:     action,
		ActionArgs: actionArgs,
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// rejectDangerousArgs blocks fclones flags that could execute arbitrary commands.
func rejectDangerousArgs(raw, envVar string) {
	args, err := parseArgs(raw)
	if err != nil {
		slog.Error("invalid argument syntax", "env", envVar, "error", err)
		os.Exit(1)
	}
	for _, arg := range args {
		lower := strings.ToLower(arg)
		if lower == "--command" || strings.HasPrefix(lower, "--command=") {
			slog.Error("dangerous flag not allowed", "flag", "--command", "env", envVar)
			os.Exit(1)
		}
	}
}

// verifyCacheDir ensures the cache directory exists and is writable.
// fclones creates a /fclones subdirectory inside this path.
func verifyCacheDir() {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		slog.Warn("failed to create cache directory", "path", cacheDir, "error", err)
		return
	}
	testFile := filepath.Join(cacheDir, ".write_test")
	f, err := os.Create(testFile)
	if err != nil {
		slog.Warn("cache directory not writable", "path", cacheDir, "uid", os.Getuid())
		return
	}
	f.Close()
	os.Remove(testFile)
	slog.Info("cache directory verified", "path", cacheDir)
}

// --- Scan Job ---

func runFclonesJob(ctx context.Context, cfg *config) {
	mu.Lock()
	if currentJob != nil {
		mu.Unlock()
		slog.Info("job already running, skipping overlapping request")
		return
	}
	// Mark job as running with a sentinel while holding the lock
	// to prevent TOCTOU races between concurrent goroutines.
	sentinel := &exec.Cmd{}
	currentJob = sentinel
	mu.Unlock()

	startTime := time.Now()
	slog.Info("scan starting", "target", cfg.ScanPath)

	// Use a unique temp file to avoid predictable path attacks.
	tmpFile, err := os.CreateTemp("", "fclones_report_*.txt")
	if err != nil {
		slog.Error("failed to create temp file", "error", err)
		clearCurrentJob()
		return
	}
	tmpPath := tmpFile.Name()

	args, err := buildScanArgs(cfg)
	if err != nil {
		slog.Error("failed to build scan args", "error", err)
		tmpFile.Close()
		os.Remove(tmpPath)
		clearCurrentJob()
		return
	}

	slog.Info("running command", "command", "fclones "+strings.Join(args, " "))

	var errBuf bytes.Buffer
	cmd := exec.CommandContext(ctx, "fclones", args...)
	cmd.Stdout = io.MultiWriter(os.Stderr, tmpFile)
	cmd.Stderr = io.MultiWriter(os.Stderr, &errBuf)

	mu.Lock()
	currentJob = cmd
	mu.Unlock()

	err = cmd.Run()
	tmpFile.Close()
	clearCurrentJob()

	duration := time.Since(startTime)

	if ctx.Err() != nil {
		slog.Info("scan interrupted by shutdown signal")
		os.Remove(tmpPath)
		return
	}

	if err != nil {
		slog.Error("scan failed", "duration", duration, "error", err,
			"stderr", errBuf.String())
		setHealthy(false)
		os.Remove(tmpPath)
		return
	}

	outputBytes, err := os.ReadFile(tmpPath)
	if err != nil {
		slog.Warn("failed to read output file", "error", err)
		outputBytes = []byte{}
	}
	outputStr := string(outputBytes)

	stats := parseStats(outputStr)
	duplicateList := parseDuplicatesFormatted(outputStr)
	hasDuplicates := duplicateList != "No duplicates found."

	slog.Info("scan complete",
		"duration", duration.Round(time.Second),
		"redundant", stats.Size,
		"groups", stats.Groups,
		"duplicates_found", hasDuplicates)

	if hasDuplicates {
		slog.Info("duplicate files found", "details", duplicateList)
	}

	runFclonesAction(ctx, cfg, tmpPath)
	os.Remove(tmpPath)
	setHealthy(true)
}

// clearCurrentJob resets the currentJob sentinel under the mutex.
func clearCurrentJob() {
	mu.Lock()
	currentJob = nil
	mu.Unlock()
}

// buildScanArgs constructs the fclones scan command arguments from config.
func buildScanArgs(cfg *config) ([]string, error) {
	args := []string{actionGroup}
	scanArgs, err := parseArgs(cfg.ScanPath)
	if err != nil {
		return nil, fmt.Errorf("invalid scan path syntax: %w", err)
	}
	args = append(args, scanArgs...)

	if cfg.Args != "" {
		extraArgs, err := parseArgs(cfg.Args)
		if err != nil {
			return nil, fmt.Errorf("invalid FCLONES_ARGS syntax: %w", err)
		}
		args = append(args, extraArgs...)
	}

	// Enable caching — fclones uses $XDG_CACHE_HOME/fclones.
	args = append(args, "--cache")
	return args, nil
}

// --- Action ---

// runFclonesAction executes the post-scan action (remove, link, dedupe) on the report file.
func runFclonesAction(ctx context.Context, cfg *config, reportPath string) {
	if cfg.Action == actionGroup || cfg.Action == "" {
		return
	}

	actionCmdArgs := []string{cfg.Action}
	if cfg.ActionArgs != "" {
		extraArgs, err := parseArgs(cfg.ActionArgs)
		if err != nil {
			slog.Error("invalid FCLONES_ACTION_ARGS syntax", "error", err)
			return
		}
		actionCmdArgs = append(actionCmdArgs, extraArgs...)
	}

	slog.Info("performing action", "command", "fclones "+strings.Join(actionCmdArgs, " "))

	inFile, err := os.Open(reportPath)
	if err != nil {
		slog.Error("failed to open report for action", "error", err)
		return
	}
	defer inFile.Close()

	var actionOutput bytes.Buffer
	actionCmd := exec.CommandContext(ctx, "fclones", actionCmdArgs...)
	actionCmd.Stdin = inFile
	actionCmd.Stdout = io.MultiWriter(os.Stderr, &actionOutput)
	actionCmd.Stderr = io.MultiWriter(os.Stderr, &actionOutput)

	if err := actionCmd.Run(); err != nil {
		slog.Error("action failed", "action", cfg.Action, "error", err,
			"output", actionOutput.String())
		return
	}

	processedLine := extractProcessedLine(actionOutput.String())
	slog.Info("action complete", "action", cfg.Action, "result", processedLine)
}

// --- Output Parsing ---

// fclonesStats holds parsed statistics from fclones output.
type fclonesStats struct {
	Groups string
	Size   string
}

func parseStats(output string) fclonesStats {
	stats := fclonesStats{Groups: "0", Size: "0 B"}

	for line := range strings.SplitSeq(output, "\n") {
		switch {
		case strings.HasPrefix(line, "# Redundant:"):
			stats.Size = parseRedundantSize(line)
		case strings.HasPrefix(line, "# Total:"):
			if parts := strings.Fields(line); len(parts) >= 2 && parts[len(parts)-1] == "groups" {
				stats.Groups = parts[len(parts)-2]
			}
		}
	}

	return stats
}

// parseRedundantSize extracts the human-readable size from a "# Redundant:" line.
// Prefers the parenthesized form "(1.2 GB)" over the bare "512 MB" form.
func parseRedundantSize(line string) string {
	if start := strings.Index(line, "("); start != -1 {
		if end := strings.Index(line, ")"); end > start {
			return line[start+1 : end]
		}
	}
	if parts := strings.Fields(line); len(parts) >= 4 {
		return parts[2] + " " + parts[3]
	}
	return "0 B"
}

func parseDuplicatesFormatted(report string) string {
	var result strings.Builder
	filesInGroup := 0

	for line := range strings.SplitSeq(report, "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}

		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			filesInGroup = 0
			continue
		}

		// Skip group header lines like "3a2b,1024 B,2 * 512 B:"
		if strings.Contains(line, ",") && strings.Contains(line, "*") && strings.HasSuffix(line, ":") {
			filesInGroup = 0
			continue
		}

		if filesInGroup == 0 {
			result.WriteString(trimmed + "\n")
		} else {
			result.WriteString("↳ " + trimmed + "\n")
		}

		filesInGroup++
	}

	if result.Len() == 0 {
		return "No duplicates found."
	}

	return result.String()
}

func extractProcessedLine(s string) string {
	var lastNonEmpty string

	for line := range strings.SplitSeq(s, "\n") {
		if idx := strings.Index(line, "Processed"); idx != -1 && strings.Contains(line, "reclaimed") {
			return line[idx:]
		}
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			lastNonEmpty = trimmed
		}
	}

	if lastNonEmpty != "" {
		return lastNonEmpty
	}
	return "(No output captured)"
}

// --- Argument Parsing ---

// parseArgs splits a string into arguments respecting quotes (single and double).
// Returns an error if quotes are not properly terminated or a trailing backslash exists.
func parseArgs(input string) ([]string, error) {
	var args []string
	var current strings.Builder
	inQuote := false
	var quoteChar rune
	escaped := false

	for _, r := range input {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}

		if r == '\\' {
			escaped = true
			continue
		}

		switch {
		case inQuote:
			if r == quoteChar {
				inQuote = false
				quoteChar = 0
			} else {
				current.WriteRune(r)
			}
		case r == '"' || r == '\'':
			inQuote = true
			quoteChar = r
		case r == ' ' || r == '\t':
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(r)
		}
	}
	if inQuote {
		return nil, fmt.Errorf("unterminated %c quote in: %s", quoteChar, input)
	}
	if escaped {
		return nil, fmt.Errorf("trailing backslash in: %s", input)
	}
	if current.Len() > 0 {
		args = append(args, current.String())
	}
	return args, nil
}
