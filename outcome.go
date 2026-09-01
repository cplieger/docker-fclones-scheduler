package main

import (
	"context"
	"errors"
	"fmt"
)

// phaseOutcome classifies the result of a subprocess execution phase.
type phaseOutcome int

var _ fmt.Stringer = phaseOutcome(0)

const (
	outcomeSuccess   phaseOutcome = iota
	outcomeTimeout                // phase context deadline exceeded, parent still alive
	outcomeShutdown               // parent context cancelled (SIGTERM/SIGINT)
	outcomeExecError              // subprocess returned non-zero for other reasons
)

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

// classifyExecOutcome is the single source of truth for the 3-way
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

// runOnceOutcome classifies how a single run-once scan (SCAN_INTERVAL=0)
// terminated: OK (exit 0), or non-zero for Failed (exec/timeout), Skipped
// (lock held elsewhere), or Interrupted (SIGTERM/SIGINT before completion).
type runOnceOutcome int

const (
	runOnceOK runOnceOutcome = iota
	runOnceFailed
	runOnceSkipped
	runOnceInterrupted
)

// classifyRunOnceOutcome's precedence is load-bearing: a real runErr outranks
// a lock skip, and a lock skip outranks an interrupt, so Interrupted is
// reported only for a run that started and neither failed nor was skipped --
// the asymmetry that lets a SIGTERM be a clean daemon stop yet a failed batch
// run for the one-shot.
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
