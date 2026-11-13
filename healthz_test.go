package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/go/health/v2"
	"github.com/cplieger/go/measure"
)

// healthzBody is the JSON body every cplieger /healthz answers with.
type healthzBody struct {
	Status  string   `json:"status"`
	Reasons []string `json:"reasons"`
}

// getHealthz serves one GET /healthz from h and decodes its answer.
func getHealthz(t *testing.T, h http.Handler) (int, healthzBody) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	var body healthzBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("GET /healthz body %q is not JSON: %v", rec.Body.String(), err)
	}
	return rec.Code, body
}

// TestServeMetricsServesHealthz pins the route on METRICS_LISTEN_ADDR: GET and
// HEAD answer 200 {"status":"ok"} after a good run, 503 with one reason after
// a failed one, and any other method gets 405.
func TestServeMetricsServesHealthz(t *testing.T) {
	st := health.NewStatus()
	cfg := &config{Mode: modeBuiltin, Interval: time.Hour, PhaseTimeout: 30 * time.Minute}
	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(t.Context())
	stop, err := serveMetrics(ctx, addr, measure.NewRegistry(metricsPrefix), newHealthz(st, cfg))
	if err != nil {
		cancel()
		t.Fatalf("serveMetrics(%q) error = %v", addr, err)
	}
	t.Cleanup(func() {
		cancel()
		stop()
	})
	client := &http.Client{Timeout: 5 * time.Second}
	url := "http://" + addr + "/healthz"

	for _, tc := range []struct {
		name       string
		wantStatus string
		wantCode   int
		healthy    bool
	}{
		{name: "fresh", healthy: true, wantCode: http.StatusOK, wantStatus: "ok"},
		{name: "failed", wantCode: http.StatusServiceUnavailable, wantStatus: "unhealthy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st.Set(tc.healthy)
			resp, err := client.Get(url)
			if err != nil {
				t.Fatalf("GET /healthz: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			raw, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			var body healthzBody
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatalf("GET /healthz body %q is not JSON: %v", raw, err)
			}
			if resp.StatusCode != tc.wantCode || body.Status != tc.wantStatus {
				t.Errorf("GET /healthz %s = %d %q, want %d %q", tc.name, resp.StatusCode, body.Status, tc.wantCode, tc.wantStatus)
			}
			for key, want := range map[string]string{
				"Content-Type":           "application/json",
				"Cache-Control":          "no-store",
				"X-Content-Type-Options": "nosniff",
			} {
				if got := resp.Header.Get(key); got != want {
					t.Errorf("GET /healthz %s = %q, want %q", key, got, want)
				}
			}
			if tc.healthy && body.Reasons != nil {
				t.Errorf("GET /healthz after a good run lists reasons %q, want none", body.Reasons)
			}
			if !tc.healthy && len(body.Reasons) != 1 {
				t.Errorf("GET /healthz after a failed run lists reasons %q, want exactly one", body.Reasons)
			}
		})
	}

	t.Run("HEAD", func(t *testing.T) {
		st.Set(true)
		resp, err := client.Head(url)
		if err != nil {
			t.Fatalf("HEAD /healthz: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("HEAD /healthz = %d, want 200", resp.StatusCode)
		}
	})
	t.Run("POST", func(t *testing.T) {
		resp, err := client.Post(url, "application/json", nil)
		if err != nil {
			t.Fatalf("POST /healthz: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("POST /healthz = %d, want 405", resp.StatusCode)
		}
	})
}

// TestHealthzNamesEachFault pins one reason per fault: a failed run and a
// good run older than the lease each answer 503 with a reason of their own.
func TestHealthzNamesEachFault(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		st := health.NewStatus()
		// Built-in mode, 1h interval and 30m timeout: a 3-hour lease.
		h := newHealthz(st, &config{Mode: modeBuiltin, Interval: time.Hour, PhaseTimeout: 30 * time.Minute})

		st.Set(false)
		code, failed := getHealthz(t, h)
		if code != http.StatusServiceUnavailable || len(failed.Reasons) != 1 {
			t.Fatalf("GET /healthz after a failed run = %d %q, want 503 with one reason", code, failed.Reasons)
		}
		st.Set(true)
		time.Sleep(3*time.Hour + time.Second)
		code, stale := getHealthz(t, h)
		if code != http.StatusServiceUnavailable || len(stale.Reasons) != 1 {
			t.Fatalf("GET /healthz past the lease = %d %q, want 503 with one reason", code, stale.Reasons)
		}
		if failed.Reasons[0] == stale.Reasons[0] {
			t.Errorf("a failed and a stale verdict both report %q, want a reason naming each fault", failed.Reasons[0])
		}
	})
}

// TestHealthzAgreesWithTheHealthSubcommand pins one deadline for two readers:
// in every mode, the lease the health subcommand arms is the one /healthz
// applies, and /healthz turns 503 just past it after a good run (never, with
// no lease). Built-in mode's lease is two intervals plus two scan timeouts;
// external and run-once modes arm none. Serial: it sets the environment both
// readers parse.
func TestHealthzAgreesWithTheHealthSubcommand(t *testing.T) {
	for _, env := range []struct {
		interval, timeout string
		lease             time.Duration
	}{
		{interval: "1h", timeout: "30m", lease: 3 * time.Hour},
		{interval: "", timeout: "", lease: 2*3*time.Hour + 2*12*time.Hour},
		{interval: "off", timeout: ""},
		{interval: "0", timeout: ""},
		{interval: "1h", timeout: "0"},
	} {
		t.Run("SCAN_INTERVAL="+env.interval+" SCAN_TIMEOUT="+env.timeout, func(t *testing.T) {
			t.Setenv("SCAN_INTERVAL", env.interval)
			t.Setenv("SCAN_TIMEOUT", env.timeout)
			cfg, err := loadConfig()
			if err != nil {
				t.Fatalf("loadConfig: %v", err)
			}
			if got := probeLease(); got != env.lease {
				t.Fatalf("the health subcommand arms %v, want %v", got, env.lease)
			}
			synctest.Test(t, func(t *testing.T) {
				st := health.NewStatus()
				h := newHealthz(st, &cfg)
				st.Set(true)
				// With no lease, any age is healthy; 100 hours stands for "long ago".
				within, past, pastCode := env.lease-time.Second, 2*time.Second, http.StatusServiceUnavailable
				if env.lease == 0 {
					within, past, pastCode = time.Hour, 100*time.Hour, http.StatusOK
				}
				time.Sleep(within)
				if code, _ := getHealthz(t, h); code != http.StatusOK {
					t.Errorf("/healthz %v after a good run = %d, want 200", within, code)
				}
				time.Sleep(past)
				if code, _ := getHealthz(t, h); code != pastCode {
					t.Errorf("/healthz past the lease = %d, want %d", code, pastCode)
				}
			})
		})
	}
}
