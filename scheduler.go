package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/cplieger/docker-fclones-scheduler/internal/args"
	"github.com/cplieger/docker-fclones-scheduler/internal/ioutil"
	"github.com/cplieger/docker-fclones-scheduler/internal/parsing"
	"github.com/cplieger/scheduler/v2"
)

// defaultCommandRunner builds the fclones subprocess commands with graceful
// shutdown — SIGTERM on context cancellation, then a DefaultGrace (5s) window
// before os/exec escalates to SIGKILL — via the shared scheduler library
// (identical to the hand-rolled runner it replaces). The caller wires
// Stdout/Stderr on the returned command before Run.
var defaultCommandRunner = scheduler.NewCommandRunner(scheduler.DefaultGrace)

// reportTempPattern is the os.CreateTemp filename pattern for the per-scan fclones
// report under /cache. cleanStaleReports globs the identical pattern to reclaim
// orphans, so the two MUST stay in lockstep -- a drift here silently stops the sweep
// from matching any orphan. Keep both call sites on this one constant.
const reportTempPattern = "fclones_report_*.txt"

// logKeyDurationS is the slog attribute key for an integer-seconds phase
// duration. It is emitted on the scan-complete, action-complete, timeout, and
// exec-error log lines so completed and failed phase durations can be charted
// from one numeric field. The shutdown (interrupted) line deliberately omits
// it: an interrupted phase never ran to a natural end, so its elapsed time is
// not a meaningful phase duration.
const logKeyDurationS = "duration_s"

// logKeyOutcome is the slog attribute key tagging the terminal outcome of a run
// or startup so a single Loki/Grafana query (outcome=~".+") catches every
// outcome on one field. It is emitted on every non-success outcome and, for the
// run-once mode where the exit code is the batch-job result, on the terminal
// success outcome too. The values:
//
//   - the classified phaseOutcome on the terminal phase lines: "timeout",
//     "exec_error", "shutdown";
//   - "lock_error" (scan lock could not be acquired) and "start_error" (bad
//     args or temp-file creation failed) for the pre-exec failures that mark a
//     run unhealthy before a phase outcome exists;
//   - "skipped" on the overlap-skip path when a job is already running;
//   - "success" on the run-once happy path ("run once complete"), the one
//     terminal success that is tagged so a one-shot's healthy completion is
//     queryable alongside its failures;
//   - on the run-once terminal paths (main.go runOnce), "shutdown" (interrupt
//     mid-scan) and "skipped" (scan lock held) are re-emitted at WARN, so a
//     one-shot's batch failure is queryable on this field; the benign daemon
//     occurrences of both values above are INFO, so level disambiguates a
//     run-once failure from a clean daemon shutdown / overlap skip;
//   - the startup-failure literals "config_error" (invalid FCLONES_ACTION /
//     FCLONES_SCAN_TIMEOUT / argument syntax / a dangerous flag), "cache_error"
//     (cache directory verification failed), and "bad_subcommand" (an unknown
//     subcommand) emitted by loadConfig/bootstrap/main before any scan runs, so
//     a config or cache fault that crash-loops the container is caught by the
//     same query rather than being silently missed.
//
// Because the run-once success line flows through this key, an
// outcome=~".+" query matches that one healthy line in addition to every
// failure; scope to outcome!="success" to view non-success outcomes only.
const logKeyOutcome = "outcome"

// cleanStaleReports removes orphaned fclones report temp files left in /cache
// by a previous scan whose `defer os.Remove` never ran -- e.g. the container
// was SIGKILLed after the 5s WaitDelay grace, OOM-killed, or lost power
// mid-scan. Because /cache is a persistent volume these orphans accumulate
// across container restarts. Intended for daemon startup; it takes the same
// advisory flock that serialises scans before sweeping, so it can never unlink
// a live report owned by a concurrent scan in any process.
func cleanStaleReports() {
	// Take the same advisory flock that serialises scans before sweeping: a
	// concurrent cross-process `wrapper scan` (e.g. an Ofelia docker exec firing
	// as the container restarts) may hold a live report file matching the glob.
	// Acquiring the lock first guarantees no scan is in flight in any process, so
	// every match is a genuine orphan. If the lock is held, skip the sweep -- the
	// orphans (if any) are reclaimed on a later startup when no scan races.
	lock, ok, lockErr := scheduler.TryLock(lockFile)
	if lockErr != nil {
		slog.Warn("cannot acquire scan lock for stale-report sweep, skipping; orphaned report temp files (if any) will be reclaimed on a later startup",
			"path", lockFile, "error", lockErr)
		return
	}
	if !ok {
		slog.Debug("scan in flight, skipping stale-report sweep")
		return
	}
	defer lock.Unlock()

	sweepStaleReports(cacheDir)
}

// sweepStaleReports removes report temp files matching reportTempPattern in dir.
// It is split out of cleanStaleReports (which acquires the scan flock before
// calling this) so a test can exercise the glob+remove against a temp dir and
// assert the create/glob lockstep on reportTempPattern. Callers MUST already
// hold the scan lock: sweepStaleReports does no locking itself and would
// otherwise race a live scan's report file.
func sweepStaleReports(dir string) {
	matches, err := filepath.Glob(filepath.Join(dir, reportTempPattern))
	if err != nil {
		return
	}
	for _, m := range matches {
		if rmErr := os.Remove(m); rmErr != nil {
			slog.Warn("failed to remove stale report temp file", "path", m, "error", rmErr)
			continue
		}
		slog.Debug("removed stale report temp file", "path", m)
	}
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

// markerAction decides how runFclonesJob's deferred health-marker update
// behaves once a run finishes. When the parent context was cancelled
// (ctxErr != nil) the run was interrupted rather than completed, so set is
// false and the marker is left untouched, preserving the last finished
// run's state. Otherwise set is true and healthy reports whether the run
// succeeded (runErr == nil).
func markerAction(ctxErr, runErr error) (set, healthy bool) {
	if ctxErr != nil {
		return false, false
	}
	return true, runErr == nil
}

// streamAttrs returns the captured-output slog attributes shared by the timeout
// and exec-error terminal outcomes: the stderr trio (value, total bytes,
// truncated) plus the stdout trio when stdout was captured (the scan phase
// streams stdout to the report file and passes nil). Factoring it out of both
// branches keeps the two terminal outcomes provably emitting one key shape.
func streamAttrs(stderr, stdout *ioutil.LimitedBuffer) []any {
	attrs := []any{
		"stderr", stderr.String(),
		"stderr_total_bytes", stderr.Total(),
		"stderr_truncated", stderr.Truncated(),
	}
	if stdout != nil {
		attrs = append(attrs,
			"stdout", stdout.String(),
			"stdout_total_bytes", stdout.Total(),
			"stdout_truncated", stdout.Truncated())
	}
	return attrs
}

// classifyAndLogOutcome classifies a finished subprocess run and logs any terminal
// (non-success) outcome with the attribute shape shared by the scan and action phases,
// returning (done, err): done is true for every outcome except outcomeSuccess, and err is the
// value the phase should return (nil for an expected shutdown). Keeping both phases on this one
// path stops their log keys, return semantics, and truncation attrs from drifting. The action
// phase passes its captured stdout buffer (the scan sends stdout to the report file, so it passes
// nil) and any phase-specific attrs via extra.
func classifyAndLogOutcome(
	parent, phaseCtx context.Context,
	log *slog.Logger,
	phase string,
	runErr error,
	timeout, duration time.Duration,
	stderr, stdout *ioutil.LimitedBuffer,
	extra ...any,
) (done bool, err error) {
	switch outcome := classifyExecOutcome(parent, phaseCtx, runErr); outcome {
	case outcomeSuccess:
		return false, nil
	case outcomeShutdown:
		attrs := make([]any, 0, 6+len(extra))
		attrs = append(attrs,
			"reason", outcome.String(), logKeyOutcome, outcome,
			"cause", context.Cause(parent),
		)
		attrs = append(attrs, extra...)
		log.Info(phase+" interrupted", attrs...)
		return true, nil
	case outcomeTimeout:
		// 10 fixed attrs + up to 12 from streamAttrs (stderr trio +
		// optional stdout trio) + caller extra; never under-allocates.
		attrs := make([]any, 0, 22+len(extra))
		attrs = append(attrs,
			"reason", outcome.String(), logKeyOutcome, outcome,
			"timeout", timeout, "duration", duration,
			logKeyDurationS, int(duration.Round(time.Second).Seconds()),
		)
		attrs = append(attrs, streamAttrs(stderr, stdout)...)
		attrs = append(attrs, extra...)
		log.Error(phase+" timeout exceeded", attrs...)
		return true, fmt.Errorf("%s timeout exceeded after %s", phase, timeout)
	case outcomeExecError:
		// 10 fixed attrs + up to 12 from streamAttrs (stderr trio +
		// optional stdout trio) + caller extra; never under-allocates.
		attrs := make([]any, 0, 22+len(extra))
		attrs = append(attrs,
			"reason", outcome.String(), logKeyOutcome, outcome,
			"duration", duration,
			logKeyDurationS, int(duration.Round(time.Second).Seconds()),
			"error", runErr,
		)
		attrs = append(attrs, streamAttrs(stderr, stdout)...)
		attrs = append(attrs, extra...)
		log.Error(phase+" failed", attrs...)
		return true, fmt.Errorf("%s exec failed: %w", phase, runErr)
	default:
		panic(fmt.Sprintf("unhandled phaseOutcome: %d", int(outcome)))
	}
}

// runFclonesJob attempts one scan+action. It returns ran=false ONLY when the
// scan lock was already held by another process (the advisory-flock overlap
// skip; with the daemon owning all in-process runs, that means a DIFFERENT
// container or a manual `docker run` sharing this /cache): no scan or action
// ran, and the caller decides what a no-op means for its mode. In every
// other outcome (success, bad args, exec failure, timeout, interrupt)
// ran=true -- the job acquired the lock and did its work or tried to. err is
// non-nil on a genuine failure; on the lock-skip and on a clean run it is
// nil, so callers that care about "did a scan actually happen" must inspect
// ran, not just err. Marker writes are the CALLER's job (the daemon's
// healthController, or runOnce directly), routed through jobHealthSignal so
// a skip writes nothing and an interrupted run leaves the last real
// outcome in place.
func runFclonesJob(ctx context.Context, cfg *config, trigger string, newCmd scheduler.CommandRunner) (ran bool, err error) {
	lock, ok, lockErr := scheduler.TryLock(lockFile)
	if lockErr != nil {
		slog.Error("cannot acquire scan lock", "trigger", trigger, logKeyOutcome, "lock_error", "path", lockFile, "error", lockErr)
		return true, lockErr
	}
	if !ok {
		slog.Info("job already running elsewhere, skipping overlapping request", "trigger", trigger, logKeyOutcome, "skipped")
		return false, nil
	}
	defer lock.Unlock()

	scanID := newScanID()
	log := slog.With("scan_id", scanID, "trigger", trigger)

	scanArgs, err := buildScanArgs(cfg)
	if err != nil {
		log.Error("scan failed to start", "reason", "bad_args", logKeyOutcome, "start_error", "error", err)
		return true, err
	}

	tmpFile, err := os.CreateTemp(cacheDir, reportTempPattern)
	if err != nil {
		log.Error("scan failed to start", "reason", "tmpfile", logKeyOutcome, "start_error", "error", err)
		return true, err
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	log.Info("scan starting",
		"target", cfg.ScanPath,
		"binary", "fclones", "args", scanArgs,
		"timeout", cfg.PhaseTimeout)

	scanCtx, cancel := phaseContext(ctx, cfg.PhaseTimeout)
	defer cancel()

	errBuf := &ioutil.LimitedBuffer{Max: streamCapBytes}
	scanFilter := ioutil.NewFilteringWriter(os.Stderr)
	cmd := newCmd(scanCtx, "fclones", scanArgs...)
	cmd.Stdout = tmpFile
	cmd.Stderr = io.MultiWriter(scanFilter, errBuf)

	startTime := time.Now()
	runErr := cmd.Run()
	duration := time.Since(startTime)
	scanFilter.Flush()
	if n := scanFilter.Floods(); n > 0 {
		log.Warn("fclones emitted a no-newline output flood; partial line force-flushed at cap",
			"flood_count", n, "cap_bytes", ioutil.MaxLineBytes)
	}
	if cerr := tmpFile.Close(); cerr != nil {
		log.Warn("failed to close report temp file", "error", cerr)
	}

	if done, phaseErr := classifyAndLogOutcome(ctx, scanCtx, log, "scan", runErr,
		cfg.PhaseTimeout, duration, errBuf, nil); done {
		return true, phaseErr
	}
	// success: continue to report parsing

	reportFile, err := os.Open(tmpPath)
	if err != nil {
		log.Error("failed to open report for decode",
			"reason", "decode_error", logKeyOutcome, "decode_error", "error", err)
		return true, err
	}
	report, decErr := parsing.DecodeReport(bufio.NewReaderSize(reportFile, 64*1024), maxLoggedGroups)
	if cerr := reportFile.Close(); cerr != nil {
		log.Warn("failed to close report file after decode", "error", cerr)
	}
	if decErr != nil {
		// The scan succeeded but its report is not the contract the wrapper
		// was built against -- upstream format drift, an interrupted write, or
		// a corrupt report. Fail the run loudly (non-zero, unhealthy marker):
		// silently degraded stats are exactly what the JSON contract rules out.
		log.Error("fclones report decode failed; failing the run",
			"reason", "decode_error", logKeyOutcome, "decode_error", "error", decErr)
		return true, fmt.Errorf("decode fclones report: %w", decErr)
	}

	hasDuplicates := report.TotalGroups > 0

	log.Info("scan complete",
		logKeyDurationS, int(duration.Round(time.Second).Seconds()),
		"redundant_human", parsing.HumanBytes(report.Stats.RedundantFileSize),
		"groups", report.TotalGroups,
		"duplicate_files", report.TotalDuplicates,
		"duplicates_found", hasDuplicates)

	if hasDuplicates {
		logDuplicateGroups(log, &report)
	}

	if !shouldRunAction(log, cfg, hasDuplicates) {
		return true, nil
	}

	return true, runFclonesAction(ctx, cfg, tmpPath, log, newCmd)
}

// shouldRunAction reports whether the dedup action phase should run after a
// scan. Report-only mode never runs an action; otherwise the action runs
// exactly when the decoded report carries duplicate groups (the decode is
// strict, so a zero here is a genuine zero -- the old drift-suspicion
// fallback died with the text parser).
func shouldRunAction(log *slog.Logger, cfg *config, hasDuplicates bool) bool {
	if cfg.Action == actionGroup {
		// Report-only: there is no action to run.
		return false
	}
	if !hasDuplicates {
		log.Info("action skipped",
			"action", cfg.Action, "reason", "no_duplicates")
		return false
	}
	return true
}

// Duplicate-detail log caps. maxLoggedGroups also bounds how many decoded
// groups DecodeReport retains in memory (the streamed totals cover the rest),
// so the caps govern memory as well as log volume.
const (
	maxLoggedGroups = 100
	maxLoggedPairs  = 500
)

// logDuplicateGroups emits one `duplicate file` Info line per (keeper,
// duplicate) pair from the report's retained groups, bounded by the pair and
// byte caps; the truncation line reports full-document totals from the
// streamed counts.
func logDuplicateGroups(log *slog.Logger, report *parsing.Report) {
	pairsEmitted := 0
	detailBytes := 0

pairs:
	for i, g := range report.Groups {
		for _, dup := range g.Duplicates {
			if pairsEmitted >= maxLoggedPairs {
				break pairs
			}
			detailBytes += len(g.Keeper) + len(dup)
			if detailBytes > logDetailCapBytes {
				log.Info("duplicate detail truncated, byte cap reached",
					"logged_pairs", pairsEmitted,
					"cap_bytes", logDetailCapBytes)
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

	if pairsEmitted < report.TotalDuplicates {
		log.Info("duplicate pairs truncated",
			"logged_pairs", pairsEmitted,
			"total_pairs", report.TotalDuplicates,
			"logged_groups", len(report.Groups),
			"total_groups", report.TotalGroups)
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

	// Wrapper-owned flags: --cache shares the fclones hash cache across runs,
	// and -f json is the report contract DecodeReport is built against. Both
	// are rejected in FCLONES_ARGS at startup (rejectWrapperOwnedArgs), so
	// user args can never fight them.
	cmdArgs = append(cmdArgs, "--cache", "-f", "json")
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

// phaseContext derives the per-phase context from the configured timeout. A
// non-positive timeout (FCLONES_SCAN_TIMEOUT=0) means "no timeout": the phase
// runs under the parent ctx so a SIGTERM still cancels it, but no deadline
// applies. A positive timeout bounds the phase. The caller must defer the
// returned cancel.
func phaseContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}

// resolveActionSummary parses the action phase's captured streams into an
// ActionSummary, preferring a recognized "Processed ... reclaimed ..." line
// from stderr (where fclones prints its summary) and falling back to stdout.
// A matched stdout summary wins over an unmatched stderr fallback line, so
// stderr diagnostics can never mask a real summary that went to stdout; when
// stderr matches nothing and is empty, stdout's parse is used either way,
// preserving the original fallback precedence. When NEITHER stream contains a
// recognizable summary but output exists, it warns with the "possible fclones
// format drift" marker: the summary parse is the action-phase analogue of the
// group-report parse, and without this warning an upstream wording change
// silently zeroes the files_deduped/bytes_reclaimed fields that log-based
// success notifications key on, while the run stays healthy.
func resolveActionSummary(log *slog.Logger, act action, stderrOut, stdoutOut string) parsing.ActionSummary {
	summary := parsing.ParseActionSummary(stderrOut)
	if !summary.Matched {
		if alt := parsing.ParseActionSummary(stdoutOut); alt.Matched || summary.RawLine == "" {
			summary = alt
		}
	}
	if !summary.Matched && summary.RawLine != "" {
		log.Warn("action summary not recognized, possible fclones format drift",
			"action", act, "result", summary.RawLine)
	}
	return summary
}

// runFclonesAction executes the post-scan action (remove, link, dedupe) on
// the report file. It returns nil on success (including the group-only case
// with no action to run, and shutdown mid-action) and a non-nil error when
// the action times out or exits non-zero.
func runFclonesAction(ctx context.Context, cfg *config, reportPath string, log *slog.Logger, newCmd scheduler.CommandRunner) error {
	actionCmdArgs, err := buildActionArgs(cfg)
	if err != nil {
		log.Error("invalid FCLONES_ACTION_ARGS syntax", logKeyOutcome, "start_error", "error", err)
		return err
	}
	if actionCmdArgs == nil {
		return nil
	}

	if ctx.Err() != nil {
		log.Info("action skipped, shutting down", logKeyOutcome, outcomeShutdown)
		return nil
	}

	actionCtx, cancel := phaseContext(ctx, cfg.PhaseTimeout)
	defer cancel()

	log.Info("performing action",
		"binary", "fclones", "args", actionCmdArgs,
		"report", reportPath)

	inFile, err := os.Open(reportPath)
	if err != nil {
		log.Error("failed to open report for action", logKeyOutcome, "start_error", "error", err)
		return err
	}
	defer inFile.Close()

	actionStdout := &ioutil.LimitedBuffer{Max: streamCapBytes}
	actionStderr := &ioutil.LimitedBuffer{Max: streamCapBytes}
	actionFilter := ioutil.NewFilteringWriter(os.Stderr)
	actionCmd := newCmd(actionCtx, "fclones", actionCmdArgs...)
	actionCmd.Stdin = inFile
	actionCmd.Stdout = actionStdout
	actionCmd.Stderr = io.MultiWriter(actionFilter, actionStderr)

	startTime := time.Now()
	runErr := actionCmd.Run()
	duration := time.Since(startTime)
	actionFilter.Flush()
	if n := actionFilter.Floods(); n > 0 {
		log.Warn("fclones emitted a no-newline output flood; partial line force-flushed at cap",
			"flood_count", n, "cap_bytes", ioutil.MaxLineBytes)
	}
	if done, phaseErr := classifyAndLogOutcome(ctx, actionCtx, log, "action", runErr,
		cfg.PhaseTimeout, duration, actionStderr, actionStdout, "action", cfg.Action); done {
		return phaseErr
	}
	// success: continue to summary parsing

	summary := resolveActionSummary(log, cfg.Action, actionStderr.String(), actionStdout.String())

	if actionStdout.Truncated() || actionStderr.Truncated() {
		log.Warn("action output exceeded capture cap; summary stats may be incomplete",
			"action", cfg.Action,
			"stdout_total_bytes", actionStdout.Total(),
			"stdout_truncated", actionStdout.Truncated(),
			"stderr_total_bytes", actionStderr.Total(),
			"stderr_truncated", actionStderr.Truncated(),
			"cap_bytes", streamCapBytes)
	}

	attrs := []any{
		"action", cfg.Action,
		logKeyDurationS, int(duration.Round(time.Second).Seconds()),
		"files_deduped", summary.Files,
		"bytes_reclaimed", summary.ReclaimedBytes,
		"reclaimed_human", parsing.HumanBytes(summary.ReclaimedBytes),
	}
	if summary.Estimated {
		// dedupe reports an advisory upper bound ("reclaimed up to X"), so mark
		// the figure as a ceiling rather than an exact reclaim.
		attrs = append(attrs, "reclaimed_estimated", true)
	}
	if summary.Files == 0 && summary.ReclaimedBytes == 0 && summary.RawLine != "" {
		attrs = append(attrs, "result", summary.RawLine)
	}
	log.Info("action complete", attrs...)
	return nil
}
