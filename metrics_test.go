package main

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/go/health"
	"github.com/cplieger/go/measure"
	"github.com/cplieger/go/scheduler"
)

func TestMetricsListenAddrTrimsInvisibleEdges(t *testing.T) {
	tests := []struct{ name, raw, want string }{
		{name: "leading space", raw: " :9469", want: ":9469"},
		{name: "zero-width edges", raw: "\u200b127.0.0.1:9469\ufeff", want: "127.0.0.1:9469"},
		{name: "blank", raw: " \t", want: ""},
		{name: "unset", raw: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("METRICS_LISTEN_ADDR", tt.raw)
			if got := metricsListenAddr(); got != tt.want {
				t.Errorf("metricsListenAddr() with %q = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

// countListens swaps listenTCP for the test and counts the binds.
func countListens(t *testing.T) *int {
	t.Helper()
	n := 0
	prev := listenTCP
	listenTCP = func(network, addr string) (net.Listener, error) {
		n++
		return prev(network, addr)
	}
	t.Cleanup(func() { listenTCP = prev })
	return &n
}

func TestServeMetricsUnsetOpensNoListener(t *testing.T) {
	binds := countListens(t)
	stop, err := serveMetrics(t.Context(), "", measure.NewRegistry(metricsPrefix))
	if err != nil {
		t.Fatalf("serveMetrics(\"\") error = %v", err)
	}
	stop()
	if *binds != 0 {
		t.Errorf("serveMetrics(\"\") bound %d listeners, want none", *binds)
	}
}

// freeAddr returns a loopback address nothing listens on.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

func TestServeMetricsServesThePage(t *testing.T) {
	addr := freeAddr(t)
	reg, _ := newReporter(&config{Action: actionLink}, health.NewLatch(health.NewMarker(filepath.Join(t.TempDir(), "healthy"))))
	stop, err := serveMetrics(t.Context(), addr, reg)
	if err != nil {
		t.Fatalf("serveMetrics(%q) error = %v", addr, err)
	}
	defer stop()
	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Get("http://" + addr + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "fclones_scheduler_last_run_ok") {
		t.Errorf("GET /metrics = %d with %q, want 200 with fclones_scheduler_last_run_ok", resp.StatusCode, body)
	}

	resp, err = client.Post("http://"+addr+"/metrics", "text/plain", nil)
	if err != nil {
		t.Fatalf("POST /metrics: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST /metrics = %d, want 405", resp.StatusCode)
	}
}

// TestServeRefusesAnAddressItCannotBind pins the refusal before any run: a
// malformed or taken METRICS_LISTEN_ADDR returns an error (run exits
// non-zero), logs one ERROR naming the variable, starts no scan and leaves
// the container unhealthy.
func TestServeRefusesAnAddressItCannotBind(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = taken.Close() })
	for name, addr := range map[string]string{"malformed": "not-a-port", "taken": taken.Addr().String()} {
		t.Run(name, func(t *testing.T) {
			logs := captureLogs(t, slog.LevelDebug)
			dir := t.TempDir()
			prevStamp := completedStampPath
			completedStampPath = filepath.Join(dir, "last-completed")
			t.Cleanup(func() { completedStampPath = prevStamp })
			marker := health.NewMarker(filepath.Join(dir, "healthy"))
			marker.Set(true)

			err := serve(t.Context(), &config{Mode: modeOnce, MetricsListenAddr: addr}, health.NewLatch(marker))

			if err == nil {
				t.Fatalf("serve with METRICS_LISTEN_ADDR=%q returned nil, want an error", addr)
			}
			out := logs.String()
			if n := strings.Count(out, "level=ERROR"); n != 1 || !strings.Contains(out, "METRICS_LISTEN_ADDR") {
				t.Errorf("logged %d ERROR lines, want one naming METRICS_LISTEN_ADDR:\n%s", n, out)
			}
			if strings.Contains(out, "scan starting") || strings.Contains(out, "container started") {
				t.Errorf("started before the bind failed:\n%s", out)
			}
			if marker.CheckHealthy() {
				t.Error("marker healthy after a refused start, want unhealthy")
			}
		})
	}
}

// gauge reads one series of the registry by its name and labels.
func gauge(t *testing.T, reg *measure.Registry, name string, labels ...string) float64 {
	t.Helper()
	families, err := reg.Gather(t.Context())
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, f := range families {
		if f.Name != metricsPrefix+"_"+name {
			continue
		}
		for _, s := range f.Samples {
			var vals []string
			for _, l := range s.Labels {
				vals = append(vals, l.Value)
			}
			if strings.Join(vals, ",") == strings.Join(labels, ",") {
				return s.Value
			}
		}
	}
	t.Fatalf("no series %s{%s}", name, strings.Join(labels, ","))
	return 0
}

// TestRestartSeedsTheTimeSeriesFromTheStamp pins that both time series survive
// a restart: before the first run, they equal the completed-run record that a
// run in the previous process wrote on /cache.
func TestRestartSeedsTheTimeSeriesFromTheStamp(t *testing.T) {
	for _, ok := range []bool{true, false} {
		t.Run("last run ok "+strconv.FormatBool(ok), func(t *testing.T) {
			dir := t.TempDir()
			prevStamp := completedStampPath
			completedStampPath = filepath.Join(dir, "last-completed")
			t.Cleanup(func() { completedStampPath = prevStamp })
			latch := health.NewLatch(health.NewMarker(filepath.Join(dir, "healthy")))
			result, runErr := resultSuccess, error(nil)
			if !ok {
				result, runErr = resultExecError, errors.New("scan failed")
			}

			_, before := newReporter(&config{}, latch)
			before.report(nil, &jobReport{ran: true, result: result, err: runErr})
			rec, known := scheduler.NewStamp(completedStampPath).Last()
			if !known || rec.OK != ok {
				t.Fatalf("completed-run record = %+v known %v, want ok %v", rec, known, ok)
			}

			reg, _ := newReporter(&config{}, latch)
			if got := gauge(t, reg, "last_run_timestamp_seconds"); got != float64(rec.Time.Unix()) {
				t.Errorf("last_run_timestamp_seconds after restart = %v, want the record's %d", got, rec.Time.Unix())
			}
			wantSuccess := float64(0)
			if ok {
				wantSuccess = float64(rec.Time.Unix())
			}
			if got := gauge(t, reg, "last_success_timestamp_seconds"); got != wantSuccess {
				t.Errorf("last_success_timestamp_seconds after restart = %v, want %v", got, wantSuccess)
			}
			if got := gauge(t, reg, "last_run_ok"); got != float64(flag(ok)) {
				t.Errorf("last_run_ok after restart = %v, want %d", got, flag(ok))
			}
		})
	}
}

// TestRunSetsEverySeriesToItsRecord runs real paths and checks that each
// series equals the field of the run finished record the same run wrote.
func TestRunSetsEverySeriesToItsRecord(t *testing.T) {
	for _, name := range []string{"action reclaims space", "action summary not recognized", "action exits non-zero", "scan exits non-zero"} {
		t.Run(name, func(t *testing.T) {
			got := runThroughNamed(t, name)
			rec, reg := got.records[0], got.reg
			num := func(field string) float64 {
				v, err := strconv.ParseFloat(rec[field], 64)
				if err != nil {
					t.Fatalf("record %s = %q: %v", field, rec[field], err)
				}
				return v
			}
			if g := gauge(t, reg, "last_run_ok"); g != num("ok") {
				t.Errorf("last_run_ok = %v, record ok = %s", g, rec["ok"])
			}
			for _, r := range runResults {
				want := float64(flag(string(r) == rec["result"]))
				if g := gauge(t, reg, "last_run_result", string(r)); g != want {
					t.Errorf("last_run_result{%s} = %v, want %v for result %s", r, g, want, rec["result"])
				}
				if g := gauge(t, reg, "runs_total", string(r)); g != want {
					t.Errorf("runs_total{%s} = %v, want %v", r, g, want)
				}
			}
			if g := gauge(t, reg, "files_processed_total"); g != num("files_processed") {
				t.Errorf("files_processed_total = %v, record %s", g, rec["files_processed"])
			}
			if g := gauge(t, reg, "reclaimed_bytes_total"); g != num("reclaimed_bytes") {
				t.Errorf("reclaimed_bytes_total = %v, record %s", g, rec["reclaimed_bytes"])
			}
			if _, ok := rec["summary_recognized"]; ok {
				if g := gauge(t, reg, "action_summary_recognized"); g != num("summary_recognized") {
					t.Errorf("action_summary_recognized = %v, record %s", g, rec["summary_recognized"])
				}
			}
			for _, pair := range [][2]string{{"duplicate_groups", "groups"}, {"duplicate_files", "duplicate_files"}, {"redundant_bytes", "redundant_bytes"}} {
				if _, ok := rec[pair[1]]; ok {
					if g := gauge(t, reg, pair[0]); g != num(pair[1]) {
						t.Errorf("%s = %v, record %s = %s", pair[0], g, pair[1], rec[pair[1]])
					}
				}
			}
			if g := gauge(t, reg, "last_run_timestamp_seconds"); g <= 0 {
				t.Errorf("last_run_timestamp_seconds = %v, want the run's time", g)
			}
			if g := gauge(t, reg, "info", modeBuiltin.String(), got.action.String()); g != 1 {
				t.Errorf("info{mode=built-in,action=%s} = %v, want 1", got.action, g)
			}
		})
	}
}
