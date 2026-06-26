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
		want     phaseOutcome
	}{
		{
			name:     "nil error returns success",
			ctx:      context.Background(),
			phaseCtx: context.Background(),
			runErr:   nil,
			want:     outcomeSuccess,
		},
		{
			name:     "phase deadline exceeded with live parent returns timeout",
			ctx:      context.Background(),
			phaseCtx: deadlineCtx(),
			runErr:   errors.New("signal: killed"),
			want:     outcomeTimeout,
		},
		{
			name:     "parent cancelled returns shutdown",
			ctx:      cancelledCtx(),
			phaseCtx: cancelledCtx(),
			runErr:   errors.New("signal: killed"),
			want:     outcomeShutdown,
		},
		{
			name:     "parent cancelled with live phase returns shutdown",
			ctx:      cancelledCtx(),
			phaseCtx: context.Background(),
			runErr:   errors.New("exit status 1"),
			want:     outcomeShutdown,
		},
		{
			name:     "generic exec error returns exec_error",
			ctx:      context.Background(),
			phaseCtx: context.Background(),
			runErr:   errors.New("exit status 1"),
			want:     outcomeExecError,
		},
		{
			name:     "nil error with cancelled parent still returns success",
			ctx:      cancelledCtx(),
			phaseCtx: context.Background(),
			runErr:   nil,
			want:     outcomeSuccess,
		},
		{
			name:     "nil error with expired phase still returns success",
			ctx:      context.Background(),
			phaseCtx: deadlineCtx(),
			runErr:   nil,
			want:     outcomeSuccess,
		},
		{
			name:     "both contexts cancelled parent wins as shutdown",
			ctx:      cancelledCtx(),
			phaseCtx: deadlineCtx(),
			runErr:   errors.New("context canceled"),
			want:     outcomeShutdown,
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

func TestPhaseOutcomeString(t *testing.T) {
	t.Parallel()
	tests := []struct {
		want    string
		outcome phaseOutcome
	}{
		{"success", outcomeSuccess},
		{"timeout", outcomeTimeout},
		{"shutdown", outcomeShutdown},
		{"exec_error", outcomeExecError},
	}
	for _, tt := range tests {
		if got := tt.outcome.String(); got != tt.want {
			t.Errorf("phaseOutcome(%d).String() = %q, want %q", int(tt.outcome), got, tt.want)
		}
	}
}

func TestPhaseOutcomeStringPanicsOnUnknown(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Error("phaseOutcome(99).String(): expected panic on unhandled value, got none")
		}
	}()
	_ = phaseOutcome(99).String()
}
