package main

import (
	"io"
	"log"
	"log/slog"
	"strings"
	"testing"
)

// restoreLogger saves slog's default logger together with the log package's
// writer and flags, restoring all three on cleanup. Use it around any code
// that installs a default logger, production setup included.
//
// slog.SetDefault also points the log package at the installed handler and
// zeroes its flags, and skips that redirect for slog's own default handler, so
// restoring slog alone leaves log writing into a dead buffer. slog goes back
// first: reinstalling a non-default prev re-runs the redirect.
func restoreLogger(t *testing.T) {
	t.Helper()
	prev, prevWriter, prevFlags := slog.Default(), log.Writer(), log.Flags()
	t.Cleanup(func() {
		slog.SetDefault(prev)
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	})
}

// captureLogs installs a text handler at level over a builder as the default
// logger for the test and returns that builder. Tests using it must NOT be
// parallel (the default logger is process-global).
func captureLogs(t *testing.T, level slog.Level) *strings.Builder {
	t.Helper()
	restoreLogger(t)
	var logs strings.Builder
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: level})))
	return &logs
}

// TestRestoreLogger_restoresLogPackage pins all three globals slog.SetDefault
// mutates. A sentinel writer and a non-zero flag set are installed first so
// neither assertion can hold by accident: the incomplete restore leaves the
// log package aimed at the capture builder with its flags zeroed, which
// silences every later slog call in the package.
func TestRestoreLogger_restoresLogPackage(t *testing.T) {
	prevWriter, prevFlags := log.Writer(), log.Flags()
	t.Cleanup(func() {
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	})
	var sentinel strings.Builder
	log.SetOutput(&sentinel)
	log.SetFlags(log.Lshortfile)

	t.Run("capture", func(t *testing.T) {
		logs := captureLogs(t, slog.LevelInfo)
		slog.Info("captured")
		if logs.Len() == 0 {
			t.Fatal("captureLogs() captured nothing; the swap itself is broken, so the restore assertions below would be vacuous")
		}
	})

	if got := log.Writer(); got != io.Writer(&sentinel) {
		t.Errorf("after captureLogs cleanup, log.Writer() = %T, want the sentinel *strings.Builder", got)
	}
	if got := log.Flags(); got != log.Lshortfile {
		t.Errorf("after captureLogs cleanup, log.Flags() = %d, want %d", got, log.Lshortfile)
	}
}
