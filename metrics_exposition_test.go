package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/docker-fclones-scheduler/internal/parsing"
	"github.com/cplieger/go/health/v2"
)

const (
	expositionGolden = "testdata/metrics.golden"
	expositionRegen  = "UPDATE_GOLDEN=1 go test . -run TestMetricsPageMatchesGolden"
)

// TestMetricsPageMatchesGolden pins the /metrics text of every family this
// app declares, byte for byte, after a fixed history: a boot from a success
// on record, one successful run and one failed run whose action summary was
// not recognized. The Go and process families are left out, since their
// values move from one scrape to the next.
func TestMetricsPageMatchesGolden(t *testing.T) {
	start := time.Date(2026, 11, 15, 3, 0, 0, 0, time.UTC)
	prevStamp := completedStampPath
	completedStampPath = filepath.Join(t.TempDir(), "last-completed")
	t.Cleanup(func() { completedStampPath = prevStamp })
	if err := os.WriteFile(completedStampPath, []byte(start.Add(-time.Hour).Format(time.RFC3339Nano)+" ok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, rep := newReporter(&deadlineConfig, health.NewStatus())
	m := rep.metrics
	m.observe(&jobReport{
		ran: true, result: resultSuccess, scanRan: true, scanDuration: 1250 * time.Millisecond,
		scan: &scanTotals{groups: 3, duplicateFiles: 12, redundantBytes: 3758096384},
		action: &actionRun{
			ran: true, summarized: true, duration: 500 * time.Millisecond,
			summary: parsing.ActionSummary{RawLine: "Processed 12 files and reclaimed 3.5 GB space", Files: 12, ReclaimedBytes: 3758096384, Matched: true},
		},
	}, true, start.Add(10*time.Minute))
	m.observe(&jobReport{
		ran: true, result: resultExecError, scanRan: true, scanDuration: 2750 * time.Millisecond,
		scan: &scanTotals{groups: 7, duplicateFiles: 20, redundantBytes: 1024},
		action: &actionRun{
			ran: true, summarized: true, duration: 125 * time.Millisecond,
			summary: parsing.ActionSummary{RawLine: "an unrecognized summary"},
		},
	}, false, start.Add(70*time.Minute))

	rec := httptest.NewRecorder()
	reg.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", http.NoBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /metrics = %d, want 200", rec.Code)
	}
	got := withoutRuntimeFamilies(rec.Body.Bytes())

	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(expositionGolden), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(expositionGolden, got, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(expositionGolden)
	if err != nil {
		t.Fatalf("read %s: %v; write it with: %s", expositionGolden, err, expositionRegen)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("/metrics differs from %s.\ngot:\n%s\nwant:\n%s\nReview the change, then regenerate with: %s",
			expositionGolden, got, want, expositionRegen)
	}
}

// withoutRuntimeFamilies drops every HELP, TYPE and sample line of the go_
// and process_ families and keeps every other line as served.
func withoutRuntimeFamilies(page []byte) []byte {
	var out bytes.Buffer
	for line := range strings.SplitAfterSeq(string(page), "\n") {
		name := line
		if rest, ok := strings.CutPrefix(line, "# HELP "); ok {
			name = rest
		} else if rest, ok := strings.CutPrefix(line, "# TYPE "); ok {
			name = rest
		}
		if strings.HasPrefix(name, "go_") || strings.HasPrefix(name, "process_") {
			continue
		}
		out.WriteString(line)
	}
	return out.Bytes()
}
