package main

import (
	"log/slog"
	"time"

	"github.com/cplieger/docker-fclones-scheduler/internal/parsing"
)

// runFinishedMsg is the message of the one record each completed run writes.
// The shipped log rules select the record by it and read its fields by name,
// so both are its contract (TestLogQLRulesReadNumericRunFinishedFields).
const runFinishedMsg = "run finished"

// runResult is how a run ended. The completed values are success and the six
// failures; shutdown is never reported because an interrupted run has no
// verdict.
type runResult string

const (
	resultSuccess     runResult = "success"
	resultExecError   runResult = "exec_error"
	resultTimeout     runResult = "timeout"
	resultDecodeError runResult = "decode_error"
	resultActionNoOp  runResult = "action_no_op"
	resultStartError  runResult = "start_error"
	resultLockError   runResult = "lock_error"
)

// scanTotals are the decoded report's duplicate totals.
type scanTotals struct {
	groups         int
	duplicateFiles int
	redundantBytes int64
}

// actionRun is what the action phase hands back to its run.
type actionRun struct {
	result   runResult
	summary  parsing.ActionSummary
	duration time.Duration
	// ran is true once the fclones action process ran; summarized once its
	// output was parsed, which is when summary is meaningful.
	ran        bool
	summarized bool
}

// jobReport is one attempted run: whether it ran, how it ended and what it
// measured. ran is false only for the cross-container lock skip.
type jobReport struct {
	err          error
	scan         *scanTotals
	action       *actionRun
	scanID       string
	trigger      string
	result       runResult
	scanDuration time.Duration
	scanRan      bool
	ran          bool
}

// ended records how the run ended and returns the report for the caller to
// hand back.
func (r *jobReport) ended(result runResult, err error) jobReport {
	r.result = result
	r.err = err
	return *r
}

// summaryRecognized reports whether the wrapper read the action's summary
// line: false exactly where resolveActionSummary warns of format drift.
func summaryRecognized(s parsing.ActionSummary) bool {
	return s.Matched || s.RawLine == ""
}

func flag(b bool) int {
	if b {
		return 1
	}
	return 0
}

// recordAttrs renders the run finished record. Every value a rule reads is a
// number. summary_recognized appears only when the action reached its summary,
// so a run with no action neither raises nor clears the drift rule.
func (r *jobReport) recordAttrs(ok bool) []any {
	attrs := []any{
		"scan_id", r.scanID, "trigger", r.trigger,
		"result", string(r.result), "ok", flag(ok),
	}
	if r.scanRan {
		attrs = append(attrs, "scan_duration_s", seconds(r.scanDuration))
	}
	if r.scan != nil {
		attrs = append(attrs,
			"groups", r.scan.groups,
			"duplicate_files", r.scan.duplicateFiles,
			"redundant_bytes", r.scan.redundantBytes)
	}
	files, reclaimed := 0, int64(0)
	if r.action != nil && r.action.ran {
		attrs = append(attrs, "action_duration_s", seconds(r.action.duration))
	}
	if r.action != nil && r.action.summarized {
		files, reclaimed = r.action.summary.Files, r.action.summary.ReclaimedBytes
		attrs = append(attrs, "summary_recognized", flag(summaryRecognized(r.action.summary)))
	}
	return append(attrs, "files_processed", files, "reclaimed_bytes", reclaimed)
}

func seconds(d time.Duration) int {
	return int(d.Round(time.Second).Seconds())
}

// reportRun writes a completed run's verdict to the health signal and to the
// run finished record in one call, so the two cannot disagree. A lock skip or
// an interrupted run writes neither; completed reports which happened.
func reportRun(ctxErr error, rep *jobReport, setHealth func(healthy bool)) (completed, healthy bool) {
	completed, healthy = jobHealthSignal(ctxErr, rep.ran, rep.err)
	if !completed {
		return false, false
	}
	setHealth(healthy)
	slog.Info(runFinishedMsg, rep.recordAttrs(healthy)...)
	return true, healthy
}
