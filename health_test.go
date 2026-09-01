package main

import (
	"context"
	"errors"
	"testing"
)

func TestJobHealthSignal(t *testing.T) {
	t.Parallel()
	tests := []struct {
		ctxErr      error
		runErr      error
		name        string
		ran         bool
		wantSet     bool
		wantHealthy bool
	}{
		{nil, nil, "clean run sets healthy", true, true, true},
		{nil, errors.New("boom"), "failed run sets unhealthy", true, true, false},
		{nil, nil, "cross-container lock skip writes nothing", false, false, false},
		{nil, errors.New("lock error"), "lock ERROR is a real failure", true, true, false},
		{context.Canceled, nil, "interrupted clean run writes nothing", true, false, false},
		{context.Canceled, errors.New("cut short"), "interrupted failed run writes nothing", true, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			set, healthy := jobHealthSignal(tt.ctxErr, tt.ran, tt.runErr)
			if set != tt.wantSet {
				t.Errorf("set = %v, want %v", set, tt.wantSet)
			}
			if set && healthy != tt.wantHealthy {
				t.Errorf("healthy = %v, want %v", healthy, tt.wantHealthy)
			}
		})
	}
}

// TestProbeOptions pins the freshness-deadline policy: armed only in
// built-in mode with a bounded phase timeout. t.Setenv forbids t.Parallel.
func TestProbeOptions(t *testing.T) {
	setEnv := func(t *testing.T, interval, timeout string) {
		t.Helper()
		t.Setenv("SCAN_INTERVAL", interval)
		t.Setenv("SCAN_TIMEOUT", timeout)
	}

	t.Run("built-in default arms a deadline", func(t *testing.T) {
		setEnv(t, "1h", "")
		if got := probeOptions(); len(got) != 1 {
			t.Errorf("probeOptions(built-in) returned %d options, want 1 (WithMaxAge armed)", len(got))
		}
	})

	t.Run("external mode keeps no deadline", func(t *testing.T) {
		setEnv(t, "off", "")
		if got := probeOptions(); got != nil {
			t.Errorf("probeOptions(external) = %v, want nil", got)
		}
	})

	t.Run("run-once keeps no deadline", func(t *testing.T) {
		setEnv(t, "0", "")
		if got := probeOptions(); got != nil {
			t.Errorf("probeOptions(once) = %v, want nil", got)
		}
	})

	t.Run("unbounded phase timeout disarms", func(t *testing.T) {
		setEnv(t, "1h", "0")
		if got := probeOptions(); got != nil {
			t.Errorf("probeOptions(timeout=0) = %v, want nil (unbounded run duration)", got)
		}
	})

	t.Run("invalid timeout disarms quietly", func(t *testing.T) {
		setEnv(t, "1h", "not-a-duration")
		if got := probeOptions(); got != nil {
			t.Errorf("probeOptions(invalid timeout) = %v, want nil", got)
		}
	})
}
