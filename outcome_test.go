package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestClassifyExecOutcome(t *testing.T) {
	t.Parallel()

	// Helper to create a cancelled context.
	cancelledCtx := func() context.Context {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx
	}

	// Helper to create a deadline-exceeded context.
	deadlineCtx := func() context.Context {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		return ctx
	}

	tests := []struct {
		ctx      context.Context
		phaseCtx context.Context
		runErr   error
		name     string
		want     PhaseOutcome
	}{
		{
			name:     "nil error returns success",
			ctx:      context.Background(),
			phaseCtx: context.Background(),
			runErr:   nil,
			want:     OutcomeSuccess,
		},
		{
			name:     "phase deadline exceeded with live parent returns timeout",
			ctx:      context.Background(),
			phaseCtx: deadlineCtx(),
			runErr:   errors.New("signal: killed"),
			want:     OutcomeTimeout,
		},
		{
			name:     "parent cancelled returns shutdown",
			ctx:      cancelledCtx(),
			phaseCtx: cancelledCtx(),
			runErr:   errors.New("signal: killed"),
			want:     OutcomeShutdown,
		},
		{
			name:     "parent cancelled with live phase returns shutdown",
			ctx:      cancelledCtx(),
			phaseCtx: context.Background(),
			runErr:   errors.New("exit status 1"),
			want:     OutcomeShutdown,
		},
		{
			name:     "generic exec error returns exec_error",
			ctx:      context.Background(),
			phaseCtx: context.Background(),
			runErr:   errors.New("exit status 1"),
			want:     OutcomeExecError,
		},
		{
			name:     "nil error with cancelled parent still returns success",
			ctx:      cancelledCtx(),
			phaseCtx: context.Background(),
			runErr:   nil,
			want:     OutcomeSuccess,
		},
		{
			name:     "nil error with expired phase still returns success",
			ctx:      context.Background(),
			phaseCtx: deadlineCtx(),
			runErr:   nil,
			want:     OutcomeSuccess,
		},
		{
			name:     "both contexts cancelled parent wins as shutdown",
			ctx:      cancelledCtx(),
			phaseCtx: deadlineCtx(),
			runErr:   errors.New("context canceled"),
			want:     OutcomeShutdown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := classifyExecOutcome(tt.ctx, tt.phaseCtx, tt.runErr)
			if got != tt.want {
				t.Errorf("classifyExecOutcome() = %v, want %v", got, tt.want)
			}
		})
	}
}
