package main

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/go/measure"
	"github.com/cplieger/go/scheduler"
)

// metricsPrefix names every series on the metrics page.
const metricsPrefix = "fclones_scheduler"

// The two phases of a run, as log lines and the phase label name them.
const (
	phaseScan   = "scan"
	phaseAction = "action"
)

// keyResult names how a run ended, as a record field and a label.
const keyResult = "result"

var (
	runResults = []runResult{
		resultSuccess, resultExecError, resultTimeout, resultDecodeError,
		resultActionNoOp, resultStartError, resultLockError,
	}
	phases = []string{phaseScan, phaseAction}
)

// runMetrics are the series each completed run sets.
type runMetrics struct {
	runs              *measure.LabeledCounter
	lastResult        *measure.LabeledGauge
	phaseDuration     *measure.LabeledGauge
	info              *measure.LabeledGauge
	lastOK            *measure.Gauge
	lastRun           *measure.Gauge
	lastSuccess       *measure.Gauge
	groups            *measure.Gauge
	duplicateFiles    *measure.Gauge
	redundantBytes    *measure.Gauge
	summaryRecognized *measure.Gauge
	filesProcessed    *measure.Counter
	reclaimedBytes    *measure.Counter
}

// newRunMetrics registers the run series on reg and seeds them: every label
// value at 0, and the last run's time, verdict and, when it succeeded, the
// success time from last, the record of the last completed run that survived
// a restart.
func newRunMetrics(reg *measure.Registry, cfg *config, last scheduler.RunRecord, known bool) *runMetrics {
	m := &runMetrics{
		runs: measure.NewLabeledCounter("runs_total",
			"Completed runs by how they ended.", []string{keyResult}),
		lastResult: measure.NewLabeledGauge("last_run_result",
			"1 for how the last completed run ended, 0 for the other results.", []string{keyResult}),
		phaseDuration: measure.NewLabeledGauge("phase_duration_seconds",
			"How long the phase took the last time it ran.", []string{"phase"}),
		info: measure.NewLabeledGauge("info",
			"Always 1; the labels name the scheduling mode and the configured action.", []string{"mode", phaseAction}),
		lastOK: measure.NewGauge("last_run_ok",
			"1 when the last completed run succeeded, else 0; the same verdict as the healthcheck."),
		lastRun: measure.NewGauge("last_run_timestamp_seconds",
			"Unix time the last run completed, read back from /cache after a restart; 0 before the first."),
		lastSuccess: measure.NewGauge("last_success_timestamp_seconds",
			"Unix time the last run succeeded; 0 until a success is known since the record on /cache."),
		groups: measure.NewGauge("duplicate_groups",
			"Duplicate groups in the last decoded scan report."),
		duplicateFiles: measure.NewGauge("duplicate_files",
			"Duplicate files, keepers excluded, in the last decoded scan report."),
		redundantBytes: measure.NewGauge("redundant_bytes",
			"Bytes the duplicates in the last decoded scan report take up."),
		summaryRecognized: measure.NewGauge("action_summary_recognized",
			"0 when the last action's summary line was not recognized, else 1."),
		filesProcessed: measure.NewCounter("files_processed_total",
			"Duplicate files the actions processed, from their summary lines."),
		reclaimedBytes: measure.NewCounter("reclaimed_bytes_total",
			"Bytes the actions reclaimed, from fclones' rounded summary; an upper bound for dedupe."),
	}
	reg.MustRegister(m.runs, m.lastResult, m.phaseDuration, m.info, m.lastOK, m.lastRun,
		m.lastSuccess, m.groups, m.duplicateFiles, m.redundantBytes, m.summaryRecognized,
		m.filesProcessed, m.reclaimedBytes)
	for _, r := range runResults {
		m.runs.Add(0, string(r))
		m.lastResult.Set(0, string(r))
	}
	for _, p := range phases {
		m.phaseDuration.Set(0, p)
	}
	m.summaryRecognized.Set(1)
	m.info.Set(1, cfg.Mode.String(), cfg.Action.String())
	if known {
		m.lastRun.Set(unixSeconds(last.Time))
		m.lastOK.Set(float64(flag(last.OK)))
		if last.OK {
			m.lastSuccess.Set(unixSeconds(last.Time))
		}
	}
	return m
}

func unixSeconds(t time.Time) float64 {
	return float64(t.Unix())
}

// observe sets the series from one completed run, the values its run finished
// record carries.
func (m *runMetrics) observe(rep *jobReport, ok bool, at time.Time) {
	m.runs.Add(1, string(rep.result))
	for _, r := range runResults {
		m.lastResult.Set(float64(flag(r == rep.result)), string(r))
	}
	m.lastOK.Set(float64(flag(ok)))
	m.lastRun.Set(unixSeconds(at))
	if ok {
		m.lastSuccess.Set(unixSeconds(at))
	}
	if rep.scanRan {
		m.phaseDuration.Set(rep.scanDuration.Seconds(), phaseScan)
	}
	if rep.scan != nil {
		m.groups.Set(float64(rep.scan.groups))
		m.duplicateFiles.Set(float64(rep.scan.duplicateFiles))
		m.redundantBytes.Set(float64(rep.scan.redundantBytes))
	}
	if rep.action == nil {
		return
	}
	if rep.action.ran {
		m.phaseDuration.Set(rep.action.duration.Seconds(), phaseAction)
	}
	if rep.action.summarized {
		m.summaryRecognized.Set(float64(flag(summaryRecognized(rep.action.summary))))
		m.filesProcessed.Add(int64(rep.action.summary.Files))
		m.reclaimedBytes.Add(rep.action.summary.ReclaimedBytes)
	}
}

// serveMetrics binds addr and serves reg's page and healthz on it until ctx
// ends; stop cancels the server and waits until it has shut down. An empty
// addr opens no listener. A bind failure is logged once, naming
// METRICS_LISTEN_ADDR, and returned so the container refuses to start rather
// than run with no page.
func serveMetrics(ctx context.Context, addr string, reg *measure.Registry, healthz http.Handler) (stop func(), err error) {
	ms, err := httpserver.ListenMetrics(ctx, addr, httpserver.MetricsHandlers{Metrics: reg.Handler(), Healthz: healthz})
	if err != nil {
		slog.Error("cannot listen on METRICS_LISTEN_ADDR; refusing to start",
			"value", addr, logKeyOutcome, "config_error", "error", err)
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := ms.Serve(ctx); err != nil {
			slog.Error("metrics listener stopped", "address", addr, "error", err)
		}
	}()
	return func() {
		cancel()
		<-done
	}, nil
}
