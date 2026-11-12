package main

import (
	"context"
	"errors"
	"testing"
	"time"
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

// TestHealthMarkerPath pins the file the image's healthcheck reads: in
// /dev/shm, which stays writable under a read-only root with no extra mount.
func TestHealthMarkerPath(t *testing.T) {
	t.Parallel()
	if want := "/dev/shm/docker-fclones-scheduler.healthy"; healthMarkerPath != want {
		t.Errorf("healthMarkerPath = %q, want %q", healthMarkerPath, want)
	}
}

// TestProbeLease pins the freshness deadline the health subcommand arms:
// two intervals plus two phase timeouts, only in built-in mode with a bounded
// phase timeout. t.Setenv forbids t.Parallel.
func TestProbeLease(t *testing.T) {
	for _, tc := range []struct {
		name, interval, timeout string
		want                    time.Duration
	}{
		{name: "built-in default", want: 2*defaultInterval + 2*defaultScanTimeout},
		{name: "two intervals and two phase timeouts", interval: "1h", timeout: "30m", want: 3 * time.Hour},
		{name: "external mode keeps no deadline", interval: "off"},
		{name: "run-once keeps no deadline", interval: "0"},
		{name: "unbounded phase timeout disarms", interval: "1h", timeout: "0"},
		{name: "invalid timeout disarms quietly", interval: "1h", timeout: "not-a-duration"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SCAN_INTERVAL", tc.interval)
			t.Setenv("SCAN_TIMEOUT", tc.timeout)
			if got := probeLease(); got != tc.want {
				t.Errorf("probeLease(SCAN_INTERVAL=%q, SCAN_TIMEOUT=%q) = %v, want %v", tc.interval, tc.timeout, got, tc.want)
			}
		})
	}
}
