package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
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

	noDuplicates   = "No duplicates found."
	defaultSizeStr = "0 B"
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

	// Validate schedule before setting up signal handling — exit immediately on bad config.
	if _, err := cron.ParseStandard(cfg.Schedule); err != nil {
		slog.Error("invalid cron schedule", "schedule", cfg.Schedule, "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Remove stale health file from a previous run that may have crashed
	// before its defer ran.
	setHealthy(false)

	c := cron.New()
	// Schedule already validated above — AddFunc cannot fail here.
	if _, err := c.AddFunc(cfg.Schedule, func() { runFclonesJob(ctx, &cfg) }); err != nil {
		panic("unreachable: cron schedule rejected after validation: " + err.Error())
	}

	// setHealthy(true) before first scan: container is alive and scheduling.
	// First scan result will update health to reflect actual scan status.
	// This avoids a start_period race where Docker kills the container
	// before the first scan completes.
	setHealthy(true)
	defer setHealthy(false)

	c.Start()
	slog.Info("container started",
		"uid", os.Getuid(), "schedule", cfg.Schedule,
		"target", cfg.ScanPath, "action", cfg.Action)

	slog.Info("triggering startup scan")
	var wg sync.WaitGroup
	wg.Go(func() { runFclonesJob(ctx, &cfg) })

	<-ctx.Done()
	slog.Info("shutting down", "cause", context.Cause(ctx))

	// Wait for cron-scheduled jobs and the startup scan goroutine.
	<-c.Stop().Done()
	wg.Wait()
}

// --- Health ---

// setHealthy creates or removes the health marker file.
func setHealthy(ok bool) {
	if ok {
		if f, err := os.Create(healthFile); err == nil {
			f.Close()
		} else {
			slog.Warn("failed to create health marker", "error", err)
		}
	} else {
		if err := os.Remove(healthFile); err != nil && !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("failed to remove health marker", "error", err)
		}
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
	if strings.EqualFold(getEnv("FCLONES_ALLOW_UNSAFE", "false"), "true") {
		slog.Warn("unsafe flags allowed, command injection guardrails disabled",
			"env", "FCLONES_ALLOW_UNSAFE")
	} else {
		rejectDangerousArgs(args, "FCLONES_ARGS")
		rejectDangerousArgs(actionArgs, "FCLONES_ACTION_ARGS")
	}

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

// dangerousFlags lists fclones flags that execute arbitrary commands.
// --command: runs a shell command on each duplicate (action subcommands).
// --transform: pipes each file through an external program before hashing (group subcommand).
// --in-place and --no-copy are only meaningful with --transform, but blocking
// them independently prevents confusion if fclones adds new semantics later.
var dangerousFlags = []string{"--command", "--transform", "--in-place", "--no-copy"}

// rejectDangerousArgs blocks fclones flags that could execute arbitrary commands.
func rejectDangerousArgs(raw, envVar string) {
	args, err := parseArgs(raw)
	if err != nil {
		slog.Error("invalid argument syntax", "env", envVar, "error", err)
		os.Exit(1)
	}
	for _, arg := range args {
		lower := strings.ToLower(arg)
		for _, flag := range dangerousFlags {
			if lower == flag || strings.HasPrefix(lower, flag+"=") {
				slog.Error("dangerous flag not allowed", "flag", flag, "env", envVar)
				os.Exit(1)
			}
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
		setHealthy(false)
		clearCurrentJob()
		return
	}
	tmpPath := tmpFile.Name()

	args, err := buildScanArgs(cfg)
	if err != nil {
		slog.Error("failed to build scan args", "error", err)
		tmpFile.Close()
		os.Remove(tmpPath)
		setHealthy(false)
		clearCurrentJob()
		return
	}

	slog.Info("running command", "command", "fclones "+strings.Join(args, " "))

	errBuf := &limitedBuffer{max: 1 << 20} // 1 MB stderr cap
	cmd := exec.CommandContext(ctx, "fclones", args...)
	cmd.Stdout = io.MultiWriter(os.Stderr, tmpFile)
	cmd.Stderr = io.MultiWriter(os.Stderr, errBuf)

	mu.Lock()
	currentJob = cmd
	mu.Unlock()

	err = cmd.Run()
	tmpFile.Close()
	clearCurrentJob()

	duration := time.Since(startTime)

	if ctx.Err() != nil {
		slog.Info("scan interrupted", "cause", context.Cause(ctx))
		os.Remove(tmpPath)
		setHealthy(false)
		return
	}

	if err != nil {
		slog.Error("scan failed", "duration", duration, "error", err,
			"stderr", errBuf.String())
		setHealthy(false)
		os.Remove(tmpPath)
		return
	}

	// Cap output file read to 50 MB to prevent OOM on huge filesystems.
	const maxOutputSize = 50 << 20
	outputBytes, err := readFileWithLimit(tmpPath, maxOutputSize)
	if err != nil {
		slog.Warn("failed to read output file", "error", err)
		outputBytes = []byte{}
	}
	outputStr := string(outputBytes)

	stats := parseStats(outputStr)
	duplicateList := parseDuplicatesFormatted(outputStr)
	hasDuplicates := duplicateList != noDuplicates

	slog.Info("scan complete",
		"duration", duration.Round(time.Second),
		"redundant", stats.Size,
		"groups", stats.Groups,
		"duplicates_found", hasDuplicates)

	if hasDuplicates {
		const maxLogSize = 64 * 1024 // 64 KB safe for Loki
		details := duplicateList
		truncated := false
		if len(details) > maxLogSize {
			details = details[:maxLogSize] + "\n... (truncated)"
			truncated = true
		}
		slog.Info("duplicate files found", "details", details, "truncated", truncated)
	}

	actionOK := runFclonesAction(ctx, cfg, tmpPath)
	os.Remove(tmpPath)
	if actionOK {
		setHealthy(true)
	} else {
		setHealthy(false)
	}
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

// buildActionArgs constructs the fclones action command arguments from config.
// Returns nil if the action is "group" or empty (no post-scan action needed).
func buildActionArgs(cfg *config) ([]string, error) {
	if cfg.Action == actionGroup || cfg.Action == "" {
		return nil, nil
	}

	args := []string{cfg.Action}
	if cfg.ActionArgs != "" {
		extraArgs, err := parseArgs(cfg.ActionArgs)
		if err != nil {
			return nil, fmt.Errorf("invalid FCLONES_ACTION_ARGS syntax: %w", err)
		}
		args = append(args, extraArgs...)
	}
	return args, nil
}

// runFclonesAction executes the post-scan action (remove, link, dedupe) on the report file.
// Returns true if the action succeeded or was not needed (group/empty action).
// The action command is not tracked in currentJob because overlap prevention is handled
// by the scan job's mutex guard, and cancellation is handled by exec.CommandContext.
func runFclonesAction(ctx context.Context, cfg *config, reportPath string) bool {
	actionCmdArgs, err := buildActionArgs(cfg)
	if err != nil {
		slog.Error("invalid FCLONES_ACTION_ARGS syntax", "error", err)
		return false
	}
	if actionCmdArgs == nil {
		return true
	}

	if ctx.Err() != nil {
		slog.Info("action skipped, shutting down")
		return false
	}

	slog.Info("performing action",
		"command", "fclones "+strings.Join(actionCmdArgs, " "),
		"report", reportPath)

	inFile, err := os.Open(reportPath)
	if err != nil {
		slog.Error("failed to open report for action", "error", err)
		return false
	}
	defer inFile.Close()

	actionOutput := &limitedBuffer{max: 1 << 20} // 1 MB cap, same as scan stderr
	actionCmd := exec.CommandContext(ctx, "fclones", actionCmdArgs...)
	actionCmd.Stdin = inFile
	actionCmd.Stdout = io.MultiWriter(os.Stderr, actionOutput)
	actionCmd.Stderr = io.MultiWriter(os.Stderr, actionOutput)

	if err := actionCmd.Run(); err != nil {
		slog.Error("action failed", "action", cfg.Action, "error", err,
			"output", actionOutput.String())
		return false
	}

	processedLine := extractProcessedLine(actionOutput.String())
	slog.Info("action complete", "action", cfg.Action, "result", processedLine)
	return true
}

// --- Output Parsing ---

// fclonesStats holds parsed statistics from fclones output.
type fclonesStats struct {
	Groups string
	Size   string
}

func parseStats(output string) fclonesStats {
	stats := fclonesStats{Groups: "0", Size: defaultSizeStr}

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
	return defaultSizeStr
}

// parseDuplicatesFormatted formats fclones group output into a human-readable
// list with "↳" prefixes for duplicate files within each group.
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
		return noDuplicates
	}

	return result.String()
}

// extractProcessedLine finds the "Processed ... reclaimed ..." summary line
// from fclones action output, falling back to the last non-empty line.
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

// --- File Helpers ---

// limitedBuffer is a bytes.Buffer that stops accumulating after max bytes.
// Excess writes are silently discarded to prevent unbounded memory growth.
type limitedBuffer struct {
	buf bytes.Buffer
	max int
}

func (lb *limitedBuffer) Write(p []byte) (int, error) {
	if lb.buf.Len() >= lb.max {
		return len(p), nil
	}
	remaining := lb.max - lb.buf.Len()
	if len(p) > remaining {
		lb.buf.Write(p[:remaining])
		return len(p), nil
	}
	return lb.buf.Write(p)
}

func (lb *limitedBuffer) String() string {
	return lb.buf.String()
}

// readFileWithLimit reads a file up to maxBytes. Returns an error if the file
// exceeds the limit or cannot be read. Uses a single file handle to avoid
// TOCTOU races between stat and read.
func readFileWithLimit(path string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("file %s is %d bytes, exceeds %d byte limit", path, info.Size(), maxBytes)
	}

	// Read maxBytes+1 to detect files that grew between Stat and ReadAll (TOCTOU).
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("file %s grew past %d byte limit during read", path, maxBytes)
	}
	return data, nil
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
