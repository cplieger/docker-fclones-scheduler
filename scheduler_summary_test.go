package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// TestResolveActionSummary pins the summary-selection precedence and the
// drift warning: stderr's matched summary wins; a matched stdout summary
// beats an unmatched stderr fallback (stderr diagnostics cannot mask a real
// stdout summary); and when neither stream contains a recognizable summary
// but output exists, exactly one "possible fclones format drift" Warn fires
// so the drift alert rules cover the action phase too. The logger is
// injected (no slog.Default swap), so the capture is race-free.
func TestResolveActionSummary(t *testing.T) {
	t.Parallel()
	const summaryLine = "Processed 5 files and reclaimed 1.2 GB space"

	tests := []struct {
		name        string
		stderrOut   string
		stdoutOut   string
		wantMatched bool
		wantFiles   int
		wantRawSub  string
		wantWarn    bool
	}{
		{
			name:        "stderr summary wins",
			stderrOut:   "noise\n" + summaryLine + "\n",
			stdoutOut:   "Processed 9 files and reclaimed 9 B space",
			wantMatched: true,
			wantFiles:   5,
			wantRawSub:  "Processed 5 files",
		},
		{
			name:        "matched stdout beats unmatched stderr noise",
			stderrOut:   "some diagnostic line\n",
			stdoutOut:   summaryLine + "\n",
			wantMatched: true,
			wantFiles:   5,
			wantRawSub:  "Processed 5 files",
		},
		{
			name:        "empty stderr falls back to stdout parse",
			stderrOut:   "",
			stdoutOut:   summaryLine,
			wantMatched: true,
			wantFiles:   5,
			wantRawSub:  "Processed 5 files",
		},
		{
			name:       "neither matches, stderr noise kept as raw, warns",
			stderrOut:  "something unexpected\n",
			stdoutOut:  "also not a summary\n",
			wantRawSub: "something unexpected",
			wantWarn:   true,
		},
		{
			name:       "neither matches, only stdout has output, warns",
			stderrOut:  "",
			stdoutOut:  "reworded summary wording\n",
			wantRawSub: "reworded summary wording",
			wantWarn:   true,
		},
		{
			name:      "no output at all stays silent",
			stderrOut: "",
			stdoutOut: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

			got := resolveActionSummary(log, actionLink, tt.stderrOut, tt.stdoutOut)

			if got.Matched != tt.wantMatched {
				t.Errorf("Matched = %v, want %v", got.Matched, tt.wantMatched)
			}
			if got.Files != tt.wantFiles {
				t.Errorf("Files = %d, want %d", got.Files, tt.wantFiles)
			}
			if tt.wantRawSub != "" && !strings.Contains(got.RawLine, tt.wantRawSub) {
				t.Errorf("RawLine = %q, want to contain %q", got.RawLine, tt.wantRawSub)
			}
			warns := strings.Count(buf.String(), "possible fclones format drift")
			wantWarns := 0
			if tt.wantWarn {
				wantWarns = 1
			}
			if warns != wantWarns {
				t.Errorf("drift warns = %d, want %d\nlog:\n%s", warns, wantWarns, buf.String())
			}
		})
	}
}
