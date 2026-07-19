package main

import (
	"context"
	"errors"
	"testing"
)

// recordingMarker is a healthMarker fake capturing every Set call.
type recordingMarker struct {
	writes []bool
}

func (m *recordingMarker) Set(healthy bool) { m.writes = append(m.writes, healthy) }

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

func TestHealthController_DrainLatch(t *testing.T) {
	t.Parallel()
	m := &recordingMarker{}
	hc := newHealthController(m)

	hc.markInitial(false) // built-in boot: unhealthy
	hc.apply(true)        // first successful run
	hc.beginDrain()       // shutdown: latch unhealthy
	hc.apply(true)        // late success must NOT restore healthy
	hc.apply(false)       // unhealthy still allowed
	hc.markInitial(true)  // no-op after drain

	want := []bool{false, true, false, false}
	if len(m.writes) != len(want) {
		t.Fatalf("marker writes = %v, want %v", m.writes, want)
	}
	for i := range want {
		if m.writes[i] != want[i] {
			t.Fatalf("marker writes = %v, want %v", m.writes, want)
		}
	}
}

func TestHealthController_MarkUnhealthyAlwaysWrites(t *testing.T) {
	t.Parallel()
	m := &recordingMarker{}
	hc := newHealthController(m)

	hc.markUnhealthy()
	hc.beginDrain()
	hc.markUnhealthy()

	if len(m.writes) != 3 { // markUnhealthy, drain latch, markUnhealthy
		t.Fatalf("marker writes = %v, want 3 unconditional unhealthy writes", m.writes)
	}
	for i, w := range m.writes {
		if w {
			t.Errorf("write %d = healthy, want every write unhealthy", i)
		}
	}
}

// TestProbeOptions pins the freshness-deadline policy: armed only in
// built-in mode with a bounded phase timeout. t.Setenv forbids t.Parallel.
func TestProbeOptions(t *testing.T) {
	setEnv := func(t *testing.T, interval, timeout string) {
		t.Helper()
		t.Setenv("FCLONES_INTERVAL", interval)
		t.Setenv("FCLONES_SCAN_TIMEOUT", timeout)
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
