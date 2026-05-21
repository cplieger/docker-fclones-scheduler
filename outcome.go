package main

import (
	"context"
	"errors"
	"fmt"
)

// --- Subprocess Outcome Classification ---

// Compile-time assertion: PhaseOutcome implements fmt.Stringer.
var _ fmt.Stringer = PhaseOutcome(0)

// PhaseOutcome classifies the result of a subprocess execution phase.
type PhaseOutcome int

const (
	OutcomeSuccess   PhaseOutcome = iota
	OutcomeTimeout                // phase context deadline exceeded, parent still alive
	OutcomeShutdown               // parent context cancelled (SIGTERM/SIGINT)
	OutcomeExecError              // subprocess returned non-zero for other reasons
)

// String returns the human-readable name of the outcome.
func (o PhaseOutcome) String() string {
	switch o {
	case OutcomeSuccess:
		return "success"
	case OutcomeTimeout:
		return "timeout"
	case OutcomeShutdown:
		return "shutdown"
	case OutcomeExecError:
		return "exec_error"
	default:
		panic(fmt.Sprintf("unhandled PhaseOutcome: %d", int(o)))
	}
}

// classifyExecOutcome determines the outcome of a subprocess run by
// inspecting the parent context, the phase-scoped context, and the
// command's error. This is the single source of truth for the 3-way
// timeout/shutdown/exec_error classification used by both scan and action phases.
func classifyExecOutcome(ctx, phaseCtx context.Context, runErr error) PhaseOutcome {
	if runErr == nil {
		return OutcomeSuccess
	}
	if errors.Is(phaseCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		return OutcomeTimeout
	}
	if ctx.Err() != nil {
		return OutcomeShutdown
	}
	return OutcomeExecError
}
