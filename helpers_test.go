package main

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/cplieger/slogx/capture"
)

// captureLogs installs a text handler at level over a builder as the default
// logger for the test and returns that builder. Tests using it must NOT be
// parallel (the default logger is process-global).
func captureLogs(t *testing.T, level slog.Level) *strings.Builder {
	t.Helper()
	var logs strings.Builder
	capture.SwapDefault(t, slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: level})))
	return &logs
}
