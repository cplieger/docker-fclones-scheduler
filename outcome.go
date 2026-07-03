package main

import (
	"context"
	"errors"
	"fmt"
)

// --- Subprocess Outcome Classification ---

// Compile-time assertion: phaseOutcome implements fmt.Stringer.
var _ fmt.Stringer = phaseOutcome(0)

// phaseOutcome classifies the result of a subprocess execution phase.
type phaseOutcome int

const (
	outcomeSuccess   phaseOutcome = iota
	outcomeTimeout                // phase context deadline exceeded, parent still alive
	outcomeShutdown               // parent context cancelled (SIGTERM/SIGINT)
	outcomeExecError              // subprocess returned non-zero for other reasons
)

// String returns the human-readable name of the outcome.
func (o phaseOutcome) String() string {
	switch o {
	case outcomeSuccess:
		return "success"
	case outcomeTimeout:
		return "timeout"
	case outcomeShutdown:
		return "shutdown"
	case outcomeExecError:
		return "exec_error"
	default:
		panic(fmt.Sprintf("unhandled phaseOutcome: %d", int(o)))
	}
}

// classifyExecOutcome determines the outcome of a subprocess run by
// inspecting the parent context, the phase-scoped context, and the
// command's error. This is the single source of truth for the 3-way
// timeout/shutdown/exec_error classification used by both scan and action phases.
func classifyExecOutcome(ctx, phaseCtx context.Context, runErr error) phaseOutcome {
	if runErr == nil {
		return outcomeSuccess
	}
	if errors.Is(phaseCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		return outcomeTimeout
	}
	if ctx.Err() != nil {
		return outcomeShutdown
	}
	return outcomeExecError
}

// --- Run-Once Terminal Outcome ---

// runOnceOutcome classifies how a single run-once scan (FCLONES_INTERVAL=0)
// terminated. Extracting it makes the process exit code -- the batch-job result
// an orchestrator keys on -- a pure function of (ran, runErr, ctxErr) instead of
// logic welded into the untested runOnce orchestration, mirroring
// classifyExecOutcome and markerAction.
type runOnceOutcome int

const (
	// runOnceOK: a scan ran to completion with no error (exit 0).
	runOnceOK runOnceOutcome = iota
	// runOnceFailed: the scan or action returned an error -- exec failure or
	// timeout (exit non-zero).
	runOnceFailed
	// runOnceSkipped: the scan lock was held by another process, so no scan ran
	// (exit non-zero: a one-shot that did nothing is not a success).
	runOnceSkipped
	// runOnceInterrupted: a SIGTERM/SIGINT cancelled the parent context before
	// the single scan completed (exit non-zero so a batch orchestrator retries).
	runOnceInterrupted
)

// classifyRunOnceOutcome maps runFclonesJob's (ran, runErr) result plus the
// parent context error to the run-once terminal outcome. The precedence is
// load-bearing: a real runErr is reported ahead of a lock skip, and a lock skip
// ahead of an interrupt, so an interrupt is reported ONLY for a run that
// actually started and neither failed nor was skipped -- the asymmetry that lets
// a SIGTERM be a clean stop for the daemon yet a failed batch run for the one-shot.
func classifyRunOnceOutcome(ran bool, runErr, ctxErr error) runOnceOutcome {
	switch {
	case runErr != nil:
		return runOnceFailed
	case !ran:
		return runOnceSkipped
	case ctxErr != nil:
		return runOnceInterrupted
	default:
		return runOnceOK
	}
}
