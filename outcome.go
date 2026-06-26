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
