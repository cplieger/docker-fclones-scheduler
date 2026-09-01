package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestClassifyExecOutcome(t *testing.T) {
	t.Parallel()

	cancelledCtx := func() context.Context {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx
	}

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
			ctx:      t.Context(),
			phaseCtx: t.Context(),
			runErr:   nil,
			want:     outcomeSuccess,
		},
		{
			name:     "phase deadline exceeded with live parent returns timeout",
			ctx:      t.Context(),
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
			phaseCtx: t.Context(),
			runErr:   errors.New("exit status 1"),
			want:     outcomeShutdown,
		},
		{
			name:     "generic exec error returns exec_error",
			ctx:      t.Context(),
			phaseCtx: t.Context(),
			runErr:   errors.New("exit status 1"),
			want:     outcomeExecError,
		},
		{
			name:     "nil error with cancelled parent still returns success",
			ctx:      cancelledCtx(),
			phaseCtx: t.Context(),
			runErr:   nil,
			want:     outcomeSuccess,
		},
		{
			name:     "nil error with expired phase still returns success",
			ctx:      t.Context(),
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

func TestClassifyRunOnceOutcome(t *testing.T) {
	t.Parallel()
	errBoom := errors.New("boom")
	tests := []struct {
		runErr error
		ctxErr error
		name   string
		want   runOnceOutcome
		ran    bool
	}{
		{nil, nil, "completed clean scan is OK", runOnceOK, true},
		{errBoom, nil, "exec error or timeout is failed", runOnceFailed, true},
		{nil, nil, "lock-held skip is skipped", runOnceSkipped, false},
		{nil, context.Canceled, "interrupt after a started scan is interrupted", runOnceInterrupted, true},
		{errBoom, context.Canceled, "a real error outranks a concurrent interrupt", runOnceFailed, true},
		{errBoom, nil, "a real error outranks a lock skip", runOnceFailed, false},
		{nil, context.Canceled, "a lock skip outranks an interrupt", runOnceSkipped, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := classifyRunOnceOutcome(tt.ran, tt.runErr, tt.ctxErr); got != tt.want {
				t.Errorf("classifyRunOnceOutcome(ran=%v, runErr=%v, ctxErr=%v) = %d, want %d",
					tt.ran, tt.runErr, tt.ctxErr, got, tt.want)
			}
		})
	}
}
