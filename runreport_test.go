package main

import (
	"bytes"
	"cmp"
	"context"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/go/health/v2"
	"github.com/cplieger/go/measure"
	"github.com/cplieger/go/scheduler"
	"github.com/cplieger/go/slogx"
	"github.com/cplieger/go/slogx/capture"
)

// Scripts standing in for the two fclones phases.
const (
	reportNoDuplicates = `printf '%s' '{"header":{"stats":{"group_count":0,"redundant_file_size":0}},"groups":[]}'`
	reportOneGroup     = `printf '%s' '{"header":{"stats":{"group_count":1,"redundant_file_size":38}},"groups":[{"files":["/scandir/a","/scandir/b"],"file_len":38}]}'`
	reportGarbled      = `printf '%s' 'not a report'`
	summaryLinked      = `printf '%s\n' 'Processed 1 files and reclaimed 38 B space' >&2`
	summaryNothing     = `printf '%s\n' 'Processed 0 files and reclaimed 0 B space' >&2`
	summaryDryRun      = `printf '%s\n' 'Would process 1 files and reclaim 38 B space' >&2`
	exitFailing        = `echo 'cannot link across filesystems' >&2; exit 3`
	hang               = `exec sleep 10`
)

// fakeFclones runs scan for the `group` phase and action for every other
// fclones subcommand.
func fakeFclones(scan, action string) scheduler.CommandRunner {
	return func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		script := action
		if len(args) > 0 && args[0] == string(actionGroup) {
			script = scan
		}
		return exec.CommandContext(ctx, "sh", "-c", script)
	}
}

// runPath is one way a run can go, with the verdict its record must carry.
type runPath struct {
	setup      func(t *testing.T, dir string)
	name       string
	scan       string
	action     string
	scanPath   string
	actionArgs string
	want       runResult
	act        action
	cancelled  bool
	completed  bool
}

var runPaths = []runPath{
	{
		name: "lock error", setup: func(_ *testing.T, dir string) { scanLockPath = filepath.Join(dir, "missing", "lock") },
		scan: reportNoDuplicates, completed: true, want: resultLockError,
	},
	{name: "lock held by another process", setup: holdScanLock, scan: reportNoDuplicates},
	{name: "unparsable scan path", scanPath: `"/scandir`, scan: reportNoDuplicates, completed: true, want: resultStartError},
	{
		name: "report file cannot be created", setup: func(_ *testing.T, dir string) { reportDir = filepath.Join(dir, "missing") },
		scan: reportNoDuplicates, completed: true, want: resultStartError,
	},
	{name: "scan exits non-zero", scan: exitFailing, completed: true, want: resultExecError},
	{name: "scan past its timeout", scan: hang, completed: true, want: resultTimeout},
	{name: "undecodable report", scan: reportGarbled, completed: true, want: resultDecodeError},
	{name: "no duplicates", scan: reportNoDuplicates, act: actionLink, completed: true, want: resultSuccess},
	{name: "report-only mode", scan: reportOneGroup, act: actionGroup, completed: true, want: resultSuccess},
	{name: "action reclaims space", scan: reportOneGroup, act: actionLink, action: summaryLinked, completed: true, want: resultSuccess},
	{name: "action summary not recognized", scan: reportOneGroup, act: actionLink, action: summaryDryRun, completed: true, want: resultSuccess},
	{name: "action exits non-zero", scan: reportOneGroup, act: actionLink, action: exitFailing, completed: true, want: resultExecError},
	{name: "action past its timeout", scan: reportOneGroup, act: actionLink, action: hang, completed: true, want: resultTimeout},
	{name: "action reclaims nothing", scan: reportOneGroup, act: actionLink, action: summaryNothing, completed: true, want: resultActionNoOp},
	{name: "unparsable action args", scan: reportOneGroup, act: actionLink, actionArgs: `"--x`, completed: true, want: resultStartError},
	{name: "shutdown before the scan", scan: reportNoDuplicates, cancelled: true},
}

// holdScanLock takes the scan lock the way another container sharing /cache
// would, for the rest of the test.
func holdScanLock(t *testing.T, _ string) {
	t.Helper()
	lock, ok, err := scheduler.TryLock(scanLockPath)
	if err != nil || !ok {
		t.Fatalf("TryLock(%q) = %v, %v; want the lock", scanLockPath, ok, err)
	}
	t.Cleanup(lock.Unlock)
}

// pathRun is what one run path left behind.
type pathRun struct {
	reg        *measure.Registry
	action     action
	output     string
	records    []map[string]string
	healthy    bool
	healthSet  bool
	preHealthy bool
}

// runThrough runs p through runFclonesJob and reporter.report with the
// production log handler, as the daemon does, and returns the records and the
// marker.
// It swaps process globals, so callers are not parallel.
func runThrough(t *testing.T, p *runPath) pathRun {
	t.Helper()
	dir := t.TempDir()
	prevLock, prevDir := scanLockPath, reportDir
	scanLockPath, reportDir = filepath.Join(dir, "lock"), dir
	t.Cleanup(func() { scanLockPath, reportDir = prevLock, prevDir })
	if p.setup != nil {
		p.setup(t, dir)
	}

	var buf bytes.Buffer
	handler, _ := slogx.NewHandler(slogx.Options{Output: &buf})
	capture.SwapDefault(t, slog.New(handler))

	marker := health.NewMarker(filepath.Join(dir, "healthy"))
	pre := p.want != resultSuccess
	marker.Set(pre)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if p.cancelled {
		cancel()
	}
	cfg := &config{ScanPath: cmp.Or(p.scanPath, "/scandir"), Action: cmp.Or(p.act, actionGroup), ActionArgs: p.actionArgs, PhaseTimeout: time.Minute}
	if p.scan == hang || p.action == hang {
		cfg.PhaseTimeout = 300 * time.Millisecond
	}

	rep := runFclonesJob(ctx, cfg, triggerInterval, fakeFclones(p.scan, p.action))
	r, reg := testReporterOn(t, marker, cfg)
	completed, _ := r.report(ctx.Err(), &rep)
	if completed != p.completed {
		t.Fatalf("reporter.report completed = %v, want %v", completed, p.completed)
	}
	healthy := marker.Healthy()
	return pathRun{reg: reg, action: cfg.Action, output: buf.String(), records: runFinishedRecords(t, buf.String()), healthy: healthy, healthSet: healthy != pre, preHealthy: pre}
}

// runFinishedRecords returns the logfmt fields of every run finished record in
// out.
// testReporter is a reporter that writes marker, a fresh registry and a
// completed-run record under the test's own directory.
func testReporter(t *testing.T, marker *health.Marker) *reporter {
	t.Helper()
	r, _ := testReporterOn(t, marker, &config{})
	return r
}

// testReporterOn is testReporter for cfg, returning the registry it sets.
func testReporterOn(t *testing.T, marker *health.Marker, cfg *config) (*reporter, *measure.Registry) {
	t.Helper()
	reg := measure.NewRegistry(metricsPrefix)
	return &reporter{
		setHealth: health.NewLatch(marker).Set,
		metrics:   newRunMetrics(reg, cfg, scheduler.RunRecord{}, false),
		completed: scheduler.NewStamp(filepath.Join(t.TempDir(), "last-completed")),
	}, reg
}

func runFinishedRecords(t *testing.T, out string) []map[string]string {
	t.Helper()
	var records []map[string]string
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		if fields := parseLogfmt(t, line); fields["msg"] == runFinishedMsg {
			records = append(records, fields)
		}
	}
	return records
}

// parseLogfmt splits one slog text line into its keys and values.
func parseLogfmt(t *testing.T, line string) map[string]string {
	t.Helper()
	fields := make(map[string]string)
	for _, kv := range logfmtPairs(t, line) {
		fields[kv[0]] = kv[1]
	}
	return fields
}

// logfmtPairs splits one slog text line into its key and value pairs, in
// order and with repeats kept.
func logfmtPairs(t *testing.T, line string) [][2]string {
	t.Helper()
	var pairs [][2]string
	for line != "" {
		key, rest, found := strings.Cut(line, "=")
		if !found {
			t.Fatalf("malformed logfmt near %q", line)
		}
		var value string
		if strings.HasPrefix(rest, `"`) {
			end := 1
			for end < len(rest) && (rest[end] != '"' || rest[end-1] == '\\') {
				end++
			}
			unquoted, err := strconv.Unquote(rest[:end+1])
			if err != nil {
				t.Fatalf("unquoting %q: %v", rest[:end+1], err)
			}
			value, rest = unquoted, rest[end+1:]
		} else {
			value, rest, _ = strings.Cut(rest, " ")
		}
		pairs = append(pairs, [2]string{key, value})
		line = strings.TrimLeft(rest, " ")
	}
	return pairs
}

// TestEveryCompletedRunWritesOneRunFinishedRecord pins the record to the
// health signal on every path a run can take: a completed run writes exactly
// one record whose ok and result agree with the marker it set, and a lock
// skip or a shutdown writes neither.
func TestEveryCompletedRunWritesOneRunFinishedRecord(t *testing.T) {
	for i := range runPaths {
		p := &runPaths[i]
		t.Run(p.name, func(t *testing.T) {
			got := runThrough(t, p)
			if !p.completed {
				if len(got.records) != 0 {
					t.Errorf("wrote %d run finished records, want none: %v", len(got.records), got.records)
				}
				if got.healthSet {
					t.Errorf("marker healthy = %v, want it left at %v", got.healthy, got.preHealthy)
				}
				return
			}
			if len(got.records) != 1 {
				t.Fatalf("wrote %d run finished records, want exactly one: %v", len(got.records), got.records)
			}
			rec := got.records[0]
			if !got.healthSet {
				t.Errorf("marker healthy = %v, want the run's verdict written over %v", got.healthy, got.preHealthy)
			}
			if want := strconv.Itoa(flag(got.healthy)); rec["ok"] != want {
				t.Errorf("record ok = %q, want %s, the health it wrote", rec["ok"], want)
			}
			if rec["result"] != string(p.want) {
				t.Errorf("record result = %q, want %q", rec["result"], p.want)
			}
			if rec["level"] != "INFO" {
				t.Errorf("record level = %q, want INFO", rec["level"])
			}
			if (rec["ok"] == "1") != (p.want == resultSuccess) {
				t.Errorf("record ok = %q with result %q; only success is ok", rec["ok"], rec["result"])
			}
		})
	}
}

// runThroughNamed runs the named completed run path and checks it wrote one
// run finished record.
func runThroughNamed(t *testing.T, name string) pathRun {
	t.Helper()
	for i := range runPaths {
		if runPaths[i].name == name {
			got := runThrough(t, &runPaths[i])
			if len(got.records) != 1 {
				t.Fatalf("%s wrote %d run finished records, want one", name, len(got.records))
			}
			return got
		}
	}
	t.Fatalf("no run path named %q", name)
	return pathRun{}
}

// recordFor returns the run finished record of the named run path.
func recordFor(t *testing.T, name string) map[string]string {
	t.Helper()
	return runThroughNamed(t, name).records[0]
}

// TestRunFinishedRecordCarriesTheRunsMeasurements pins the record's numbers
// to what the fake fclones reported.
func TestRunFinishedRecordCarriesTheRunsMeasurements(t *testing.T) {
	t.Run("an action that reclaimed space", func(t *testing.T) {
		rec := recordFor(t, "action reclaims space")
		want := map[string]string{
			"groups": "1", "duplicate_files": "1", "redundant_bytes": "38",
			"files_processed": "1", "reclaimed_bytes": "38", "summary_recognized": "1",
			"trigger": triggerInterval,
		}
		for k, v := range want {
			if rec[k] != v {
				t.Errorf("record %s = %q, want %q", k, rec[k], v)
			}
		}
		for _, k := range []string{"scan_id", "scan_duration_s", "action_duration_s"} {
			if rec[k] == "" {
				t.Errorf("record has no %s: %v", k, rec)
			}
		}
	})
	t.Run("an unrecognized summary", func(t *testing.T) {
		rec := recordFor(t, "action summary not recognized")
		if rec["summary_recognized"] != "0" || rec["files_processed"] != "0" {
			t.Errorf("record summary_recognized=%q files_processed=%q, want 0 and 0", rec["summary_recognized"], rec["files_processed"])
		}
	})
	t.Run("a run with no action", func(t *testing.T) {
		rec := recordFor(t, "no duplicates")
		if _, ok := rec["summary_recognized"]; ok {
			t.Errorf("record carries summary_recognized=%q, want it absent when no action ran", rec["summary_recognized"])
		}
		if rec["files_processed"] != "0" {
			t.Errorf("record files_processed = %q, want 0", rec["files_processed"])
		}
	})
	t.Run("a scan that never finished", func(t *testing.T) {
		rec := recordFor(t, "scan exits non-zero")
		for _, k := range []string{"groups", "duplicate_files", "redundant_bytes"} {
			if _, ok := rec[k]; ok {
				t.Errorf("record carries %s=%q, want it absent when no report decoded", k, rec[k])
			}
		}
	})
}

// TestFailingRunLinesCarryOneOutcomeKey pins one key per fact: every line of
// a failing or interrupted run that names its outcome does so once, and
// carries a reason only where the reason says more than the outcome.
func TestFailingRunLinesCarryOneOutcomeKey(t *testing.T) {
	for i := range runPaths {
		p := &runPaths[i]
		if p.want == resultSuccess {
			continue
		}
		t.Run(p.name, func(t *testing.T) {
			got := runThrough(t, p)
			tagged := 0
			for line := range strings.SplitSeq(strings.TrimSpace(got.output), "\n") {
				var outcomes, reasons []string
				for _, kv := range logfmtPairs(t, line) {
					switch kv[0] {
					case logKeyOutcome:
						outcomes = append(outcomes, kv[1])
					case "reason":
						reasons = append(reasons, kv[1])
					}
				}
				if len(outcomes) == 0 {
					continue
				}
				tagged++
				if len(outcomes) != 1 {
					t.Errorf("line carries outcome %d times: %s", len(outcomes), line)
				}
				for _, r := range reasons {
					if r == outcomes[0] {
						t.Errorf("line carries reason=%s beside the equal outcome: %s", r, line)
					}
				}
			}
			if tagged == 0 {
				t.Errorf("no line names the outcome:\n%s", got.output)
			}
		})
	}
}
