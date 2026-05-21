package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"fclones-wrapper/internal/args"
	"fclones-wrapper/internal/ioutil"
	"fclones-wrapper/internal/parsing"
)

// commandRunner creates a configured *exec.Cmd for the given context and
// arguments. It decouples orchestration from subprocess construction,
// allowing tests to inject a fake runner.
type commandRunner func(ctx context.Context, name string, args ...string) *exec.Cmd

// defaultCommandRunner returns an exec.Cmd with graceful shutdown:
// SIGTERM on context cancellation with a 5s grace period before SIGKILL.
func defaultCommandRunner(ctx context.Context, name string, cmdArgs ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, cmdArgs...)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

// --- Job Slot ---

// jobSlot guards single-scan concurrency. Only one scan may run at a time.
type jobSlot struct {
	mu      sync.Mutex
	running bool
}

// TryAcquire atomically claims the single-scan mutex slot.
// Returns false if a scan is already in flight.
func (js *jobSlot) TryAcquire() bool {
	js.mu.Lock()
	defer js.mu.Unlock()
	if js.running {
		return false
	}
	js.running = true
	return true
}

// Release marks the single-scan slot as free.
func (js *jobSlot) Release() {
	js.mu.Lock()
	js.running = false
	js.mu.Unlock()
}

// --- Scan Job ---

// newScanID returns a short random hex token used to correlate log lines
// from a single scan run in Loki.
func newScanID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b[:])
}

func runFclonesJob(ctx context.Context, marker *healthMarker, cfg *config, trigger string, js *jobSlot, newCmd commandRunner) {
	if !js.TryAcquire() {
		slog.Info("job already running, skipping overlapping request", "trigger", trigger)
		return
	}
	defer js.Release()

	healthy := false
	defer func() {
		if ctx.Err() != nil {
			marker.Set(false)
			return
		}
		marker.Set(healthy)
	}()

	scanID := newScanID()
	log := slog.With("scan_id", scanID, "trigger", trigger)

	startTime := time.Now()

	scanArgs, err := buildScanArgs(cfg)
	if err != nil {
		log.Error("scan failed to start", "reason", "bad_args", "error", err)
		return
	}

	tmpFile, err := os.CreateTemp("", "fclones_report_*.txt")
	if err != nil {
		log.Error("scan failed to start", "reason", "tmpfile", "error", err)
		return
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	log.Info("scan starting",
		"target", cfg.ScanPath,
		"binary", "fclones", "args", scanArgs,
		"timeout", cfg.PhaseTimeout)

	scanCtx, cancel := context.WithTimeout(ctx, cfg.PhaseTimeout)
	defer cancel()

	errBuf := &ioutil.LimitedBuffer{Max: stderrCapBytes}
	scanFilter := ioutil.NewFilteringWriter(os.Stderr)
	cmd := newCmd(scanCtx, "fclones", scanArgs...)
	cmd.Stdout = tmpFile
	cmd.Stderr = io.MultiWriter(scanFilter, errBuf)

	runErr := cmd.Run()
	scanFilter.Flush()
	if cerr := tmpFile.Close(); cerr != nil {
		log.Warn("failed to close report temp file", "error", cerr)
	}

	duration := time.Since(startTime)

	outcome := classifyExecOutcome(ctx, scanCtx, runErr)
	switch outcome {
	case OutcomeTimeout:
		log.Error("scan timeout exceeded", "reason", "timeout",
			"outcome", outcome,
			"timeout", cfg.PhaseTimeout, "duration", duration,
			"stderr", errBuf.String(),
			"stderr_total_bytes", errBuf.Total(),
			"stderr_truncated", errBuf.Truncated())
		return
	case OutcomeShutdown:
		log.Info("scan interrupted", "reason", "shutdown",
			"outcome", outcome,
			"cause", context.Cause(ctx))
		return
	case OutcomeExecError:
		log.Error("scan failed", "reason", "exec_error",
			"outcome", outcome,
			"duration", duration, "error", runErr,
			"stderr", errBuf.String(),
			"stderr_total_bytes", errBuf.Total(),
			"stderr_truncated", errBuf.Truncated())
		return
	case OutcomeSuccess:
		// continue to report parsing
	default:
		panic(fmt.Sprintf("unhandled PhaseOutcome: %d", int(outcome)))
	}

	outputBytes, err := ioutil.ReadFileWithLimit(tmpPath, outputCapBytes)
	reportParsed := err == nil
	if !reportParsed {
		log.Error("report too large to parse, observability degraded",
			"error", err, "cap_bytes", outputCapBytes)
		outputBytes = []byte{}
	}
	outputStr := string(outputBytes)

	stats := parsing.ParseStats(outputStr)
	groups := parsing.ParseDuplicateGroups(outputStr)
	hasDuplicates := len(groups) > 0

	log.Info("scan complete",
		"duration_s", int(duration.Round(time.Second).Seconds()),
		"redundant_human", stats.Size,
		"groups", len(groups),
		"duplicate_files", countDuplicateFiles(groups),
		"duplicates_found", hasDuplicates,
		"report_parsed", reportParsed)

	if reportParsed && hasDuplicates {
		logDuplicateGroups(log, groups)
	}

	if !hasDuplicates {
		if cfg.Action != actionGroup {
			log.Info("action skipped",
				"action", cfg.Action, "reason", "no_duplicates")
		}
		healthy = true
		return
	}

	if runFclonesAction(ctx, cfg, tmpPath, log, newCmd) {
		healthy = true
	}
}

// countDuplicateFiles returns the total number of duplicate (non-keeper)
// files across all groups.
func countDuplicateFiles(groups []parsing.DuplicateGroup) int {
	n := 0
	for _, g := range groups {
		n += len(g.Duplicates)
	}
	return n
}

// logDuplicateGroups emits one `duplicate file` Info line per (keeper, duplicate) pair.
func logDuplicateGroups(log *slog.Logger, groups []parsing.DuplicateGroup) {
	const (
		maxLoggedGroups = 100
		maxLoggedPairs  = 500
	)

	emit := min(len(groups), maxLoggedGroups)
	pairsEmitted := 0

pairs:
	for i := range emit {
		g := groups[i]
		for _, dup := range g.Duplicates {
			if pairsEmitted >= maxLoggedPairs {
				break pairs
			}
			log.Info("duplicate file",
				"group", i+1,
				"keeper", g.Keeper,
				"duplicate", dup,
				"size", g.SizePerDup)
			pairsEmitted++
		}
	}

	totalPairs := countDuplicateFiles(groups)
	if pairsEmitted < totalPairs {
		log.Info("duplicate pairs truncated",
			"logged_pairs", pairsEmitted,
			"total_pairs", totalPairs,
			"logged_groups", emit,
			"total_groups", len(groups))
	}
}

// buildScanArgs constructs the fclones scan command arguments from config.
func buildScanArgs(cfg *config) ([]string, error) {
	cmdArgs := []string{string(actionGroup)}
	scanArgs, err := args.Parse(cfg.ScanPath)
	if err != nil {
		return nil, fmt.Errorf("invalid scan path syntax: %w", err)
	}
	cmdArgs = append(cmdArgs, scanArgs...)

	if cfg.Args != "" {
		extraArgs, err := args.Parse(cfg.Args)
		if err != nil {
			return nil, fmt.Errorf("invalid FCLONES_ARGS syntax: %w", err)
		}
		cmdArgs = append(cmdArgs, extraArgs...)
	}

	cmdArgs = append(cmdArgs, "--cache")
	return cmdArgs, nil
}

// --- Action ---

// buildActionArgs constructs the fclones action command arguments from config.
func buildActionArgs(cfg *config) ([]string, error) {
	if cfg.Action == actionGroup {
		return nil, nil
	}

	cmdArgs := []string{string(cfg.Action)}
	if cfg.ActionArgs != "" {
		extraArgs, err := args.Parse(cfg.ActionArgs)
		if err != nil {
			return nil, fmt.Errorf("invalid FCLONES_ACTION_ARGS syntax: %w", err)
		}
		cmdArgs = append(cmdArgs, extraArgs...)
	}
	return cmdArgs, nil
}

// runFclonesAction executes the post-scan action (remove, link, dedupe) on the report file.
func runFclonesAction(ctx context.Context, cfg *config, reportPath string, log *slog.Logger, newCmd commandRunner) bool {
	actionCmdArgs, err := buildActionArgs(cfg)
	if err != nil {
		log.Error("invalid FCLONES_ACTION_ARGS syntax", "error", err)
		return false
	}
	if actionCmdArgs == nil {
		return true
	}

	if ctx.Err() != nil {
		log.Info("action skipped, shutting down")
		return false
	}

	actionCtx, cancel := context.WithTimeout(ctx, cfg.PhaseTimeout)
	defer cancel()

	log.Info("performing action",
		"binary", "fclones", "args", actionCmdArgs,
		"report", reportPath)

	inFile, err := os.Open(reportPath)
	if err != nil {
		log.Error("failed to open report for action", "error", err)
		return false
	}
	defer inFile.Close()

	actionStdout := &ioutil.LimitedBuffer{Max: stderrCapBytes}
	actionStderr := &ioutil.LimitedBuffer{Max: stderrCapBytes}
	actionFilter := ioutil.NewFilteringWriter(os.Stderr)
	actionCmd := newCmd(actionCtx, "fclones", actionCmdArgs...)
	actionCmd.Stdin = inFile
	actionCmd.Stdout = actionStdout
	actionCmd.Stderr = io.MultiWriter(actionFilter, actionStderr)

	runErr := actionCmd.Run()
	actionFilter.Flush()
	outcome := classifyExecOutcome(ctx, actionCtx, runErr)
	switch outcome {
	case OutcomeTimeout:
		log.Error("action timeout exceeded",
			"reason", "timeout",
			"outcome", outcome,
			"action", cfg.Action, "timeout", cfg.PhaseTimeout,
			"stderr", actionStderr.String(),
			"stderr_total_bytes", actionStderr.Total(),
			"stderr_truncated", actionStderr.Truncated())
		return false
	case OutcomeShutdown:
		log.Info("action interrupted", "reason", "shutdown",
			"outcome", outcome,
			"cause", context.Cause(ctx))
		return false
	case OutcomeExecError:
		log.Error("action failed",
			"reason", "exec_error",
			"outcome", outcome,
			"action", cfg.Action, "error", runErr,
			"stderr", actionStderr.String(),
			"stderr_total_bytes", actionStderr.Total(),
			"stderr_truncated", actionStderr.Truncated(),
			"stdout", actionStdout.String(),
			"stdout_total_bytes", actionStdout.Total(),
			"stdout_truncated", actionStdout.Truncated())
		return false
	case OutcomeSuccess:
		// continue to summary parsing
	default:
		panic(fmt.Sprintf("unhandled PhaseOutcome: %d", int(outcome)))
	}

	summary := parsing.ParseActionSummary(actionStderr.String())
	if summary.RawLine == "" {
		summary = parsing.ParseActionSummary(actionStdout.String())
	}

	attrs := []any{
		"action", cfg.Action,
		"files_deduped", summary.Files,
		"bytes_reclaimed", summary.ReclaimedBytes,
		"reclaimed_human", parsing.HumanBytes(summary.ReclaimedBytes),
	}
	if summary.Files == 0 && summary.ReclaimedBytes == 0 && summary.RawLine != "" {
		attrs = append(attrs, "result", summary.RawLine)
	}
	log.Info("action complete", attrs...)
	return true
}
