package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/docker-fclones-scheduler/internal/ioutil"
	"github.com/cplieger/docker-fclones-scheduler/internal/parsing"
	"pgregory.net/rapid"
)

// --- Tests: buildScanArgs ---

func TestBuildScanArgs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		want    []string
		cfg     config
		wantLen int
		wantErr bool
	}{
		{
			name:    "basic config",
			cfg:     config{ScanPath: "/data", Args: "--min-size 1024"},
			want:    []string{"group", "/data", "--min-size", "1024", "--cache", "-f", "json"},
			wantLen: 7,
		},
		{
			name:    "invalid scan path quotes",
			cfg:     config{ScanPath: `"/unclosed`, Args: ""},
			wantErr: true,
		},
		{
			name:    "invalid args quotes",
			cfg:     config{ScanPath: "/data", Args: `--flag "unclosed`},
			wantErr: true,
		},
		{
			name:    "empty args",
			cfg:     config{ScanPath: "/data", Args: ""},
			wantLen: 5,
		},
		{
			name:    "extra args",
			cfg:     config{ScanPath: "/data", Args: "--min-size 1M --threads 4"},
			wantLen: 9,
		},
		{
			name:    "invalid scan path unterminated",
			cfg:     config{ScanPath: `"unterminated`},
			wantErr: true,
		},
		{
			name:    "invalid extra args unterminated",
			cfg:     config{ScanPath: "/data", Args: `"unterminated`},
			wantErr: true,
		},
		{
			name:    "multiple paths",
			cfg:     config{ScanPath: "/data /media /backup", Args: ""},
			want:    []string{"group", "/data", "/media", "/backup", "--cache", "-f", "json"},
			wantLen: 7,
		},
	}

	// wrapperOwnedSuffix is the wrapper-appended tail every scan invocation
	// must end with: the shared cache flag plus the JSON report contract.
	wrapperOwnedSuffix := []string{"--cache", "-f", "json"}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := buildScanArgs(&tt.cfg)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got[0] != "group" {
				t.Errorf("first arg = %q, want \"group\"", got[0])
			}
			if len(got) < 4 || !slices.Equal(got[len(got)-3:], wrapperOwnedSuffix) {
				t.Errorf("args = %v, want them to end with %v", got, wrapperOwnedSuffix)
			}
			if tt.wantLen > 0 && len(got) != tt.wantLen {
				t.Errorf("len = %d, want %d: %v", len(got), tt.wantLen, got)
			}
			if tt.want != nil && !slices.Equal(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// --- Tests: buildActionArgs ---

func TestBuildActionArgs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		want    []string
		cfg     config
		wantNil bool
		wantErr bool
	}{
		{
			name:    "group action returns nil",
			cfg:     config{Action: actionGroup},
			wantNil: true,
		},
		{
			name: "remove action no extra args",
			cfg:  config{Action: actionRemove},
			want: []string{"remove"},
		},
		{
			name: "link action with extra args",
			cfg:  config{Action: actionLink, ActionArgs: "--soft"},
			want: []string{"link", "--soft"},
		},
		{
			name: "dedupe action with multiple args",
			cfg:  config{Action: actionDedupe, ActionArgs: "--rf-over 1 --dry-run"},
			want: []string{"dedupe", "--rf-over", "1", "--dry-run"},
		},
		{
			name:    "invalid action args quotes",
			cfg:     config{Action: actionRemove, ActionArgs: `"unterminated`},
			wantErr: true,
		},
		{
			name: "action args with quoted path",
			cfg:  config{Action: actionRemove, ActionArgs: `--path "/my dir"`},
			want: []string{"remove", "--path", "/my dir"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildActionArgs(&tt.cfg)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantNil {
				if got != nil {
					t.Errorf("expected nil, got %v", got)
				}
				return
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBuildActionArgsAllActions(t *testing.T) {
	t.Parallel()
	for _, action := range []action{actionRemove, actionLink, actionDedupe} {
		t.Run(string(action), func(t *testing.T) {
			cfg := &config{Action: action}
			got, err := buildActionArgs(cfg)
			if err != nil {
				t.Fatalf("buildActionArgs(%q): unexpected error: %v", action, err)
			}
			if len(got) != 1 {
				t.Fatalf("buildActionArgs(%q) = %v, want [%q]", action, got, action)
			}
			if got[0] != string(action) {
				t.Errorf("buildActionArgs(%q)[0] = %q, want %q", action, got[0], action)
			}
		})
	}
}

// --- Property-based tests ---

func TestProperty_BuildActionArgsNilForGroup(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		cfg := &config{Action: actionGroup}
		got, err := buildActionArgs(cfg)
		if err != nil {
			rt.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			rt.Fatalf("expected nil for action %q, got %v", cfg.Action, got)
		}
	})
}

func TestProperty_BuildActionArgsStartsWithAction(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		action := rapid.SampledFrom([]action{actionRemove, actionLink, actionDedupe}).Draw(rt, "action")
		numArgs := rapid.IntRange(0, 5).Draw(rt, "numArgs")
		argTokens := make([]string, numArgs)
		for i := range numArgs {
			argTokens[i] = rapid.StringMatching(`--[a-z\-]{1,15}`).Draw(rt, "arg")
		}
		actionArgs := strings.Join(argTokens, " ")

		cfg := &config{Action: action, ActionArgs: actionArgs}
		got, err := buildActionArgs(cfg)
		if err != nil {
			rt.Fatalf("unexpected error: %v", err)
		}
		if got == nil {
			rt.Fatalf("expected non-nil for action %q", action)
		}
		if got[0] != string(action) {
			rt.Fatalf("first arg = %q, want %q", got[0], action)
		}
		if len(got) != 1+numArgs {
			rt.Fatalf("got %d args, want %d", len(got), 1+numArgs)
		}
	})
}

func TestProperty_BuildScanArgsStructure(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		scanPath := rapid.StringMatching(`/[a-z]{1,10}(/[a-z]{1,10}){0,3}`).Draw(rt, "scanPath")
		numArgs := rapid.IntRange(0, 3).Draw(rt, "numArgs")
		argTokens := make([]string, numArgs*2)
		for i := 0; i < numArgs*2; i += 2 {
			argTokens[i] = rapid.StringMatching(`--[a-z\-]{1,10}`).Draw(rt, "flag")
			argTokens[i+1] = rapid.StringMatching(`[a-zA-Z0-9]{1,10}`).Draw(rt, "value")
		}
		argsStr := strings.Join(argTokens, " ")

		cfg := &config{ScanPath: scanPath, Args: argsStr}
		got, err := buildScanArgs(cfg)
		if err != nil {
			rt.Fatalf("buildScanArgs(%+v): unexpected error: %v", cfg, err)
		}

		if got[0] != "group" {
			rt.Fatalf("buildScanArgs: first arg = %q, want \"group\"", got[0])
		}
		if len(got) < 4 || !slices.Equal(got[len(got)-3:], []string{"--cache", "-f", "json"}) {
			rt.Fatalf("buildScanArgs: args = %v, want the wrapper-owned suffix [--cache -f json]", got)
		}
	})
}

func TestNewScanIDFormat(t *testing.T) {
	t.Parallel()
	id := newScanID()
	if len(id) != 8 {
		t.Fatalf("newScanID() = %q (len %d), want 8 hex chars", id, len(id))
	}
	for _, r := range id {
		if !strings.ContainsRune("0123456789abcdef", r) {
			t.Errorf("newScanID() = %q contains non-lowercase-hex rune %q", id, r)
		}
	}
}

func TestDefaultCommandRunnerGracefulShutdown(t *testing.T) {
	t.Parallel()
	cmd := defaultCommandRunner(t.Context(), "fclones", "group", "/scandir")
	if cmd.WaitDelay != 5*time.Second {
		t.Errorf("WaitDelay = %s, want 5s", cmd.WaitDelay)
	}
	if cmd.Cancel == nil {
		t.Error("Cancel = nil, want a SIGTERM cancel func")
	}
	if len(cmd.Args) != 3 || cmd.Args[0] != "fclones" || cmd.Args[1] != "group" || cmd.Args[2] != "/scandir" {
		t.Errorf("Args = %v, want [fclones group /scandir]", cmd.Args)
	}
}

func TestDefaultCommandRunnerCancelSendsSIGTERM(t *testing.T) {
	t.Parallel()
	// The graceful-shutdown contract: cancelling the context SIGTERMs the
	// child (not the default SIGKILL), giving fclones the 5s WaitDelay grace
	// to flush its cache. A trivial sleep child makes this deterministic.
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skipf("sleep not available: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cmd := defaultCommandRunner(ctx, "sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	cancel() // fires cmd.Cancel, which signals SIGTERM
	err := cmd.Wait()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("Wait() error = %v, want an *exec.ExitError from a signalled process", err)
	}
	ws, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok {
		t.Fatalf("exit status type = %T, want syscall.WaitStatus", exitErr.Sys())
	}
	if !ws.Signaled() || ws.Signal() != syscall.SIGTERM {
		t.Errorf("child terminated by signaled=%v signal=%v, want SIGTERM (graceful shutdown)",
			ws.Signaled(), ws.Signal())
	}
}

func TestMarkerAction(t *testing.T) {
	t.Parallel()
	tests := []struct {
		ctxErr      error
		runErr      error
		name        string
		wantSet     bool
		wantHealthy bool
	}{
		{nil, nil, "clean run sets healthy", true, true},
		{nil, context.DeadlineExceeded, "failed run sets unhealthy", true, false},
		{context.Canceled, nil, "interrupted clean run leaves marker untouched", false, false},
		{context.Canceled, context.DeadlineExceeded, "interrupted failed run leaves marker untouched", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			set, healthy := markerAction(tt.ctxErr, tt.runErr)
			if set != tt.wantSet {
				t.Errorf("markerAction set=%v, want %v", set, tt.wantSet)
			}
			if set && healthy != tt.wantHealthy {
				t.Errorf("markerAction healthy=%v, want %v", healthy, tt.wantHealthy)
			}
		})
	}
}

// --- Tests: logDuplicateGroups ---

// makeDupReport builds a parsing.Report with n RETAINED groups of
// dupsPerGroup duplicates each, whose document totals equal the retained
// content (pass overrides to model a retention-capped decode).
func makeDupReport(n, dupsPerGroup, pathLen int) *parsing.Report {
	groups := make([]parsing.DuplicateGroup, n)
	for i := range groups {
		dups := make([]string, dupsPerGroup)
		for j := range dups {
			dups[j] = strings.Repeat("d", pathLen)
		}
		groups[i] = parsing.DuplicateGroup{
			Keeper:     strings.Repeat("k", pathLen),
			SizePerDup: "1 KB",
			Duplicates: dups,
		}
	}
	return &parsing.Report{
		Groups:          groups,
		TotalGroups:     n,
		TotalDuplicates: n * dupsPerGroup,
	}
}

func TestLogDuplicateGroups(t *testing.T) {
	t.Parallel()

	const dupFileMsg = `msg="duplicate file"`

	t.Run("emits every pair when under all caps", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, nil))

		logDuplicateGroups(log, makeDupReport(3, 2, 8))

		out := buf.String()
		if got := strings.Count(out, dupFileMsg); got != 6 {
			t.Errorf("emitted %d duplicate-file lines, want 6", got)
		}
		if strings.Contains(out, "truncated") {
			t.Error("unexpected truncation message for an under-cap input")
		}
	})

	t.Run("stops at the byte cap before the pair cap", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, nil))

		logDuplicateGroups(log, makeDupReport(1, 100, 1024))

		out := buf.String()
		if emitted := strings.Count(out, dupFileMsg); emitted == 0 || emitted >= 100 {
			t.Errorf("emitted %d duplicate-file lines, want a byte-capped count in (0,100)", emitted)
		}
		if !strings.Contains(out, "duplicate detail truncated, byte cap reached") {
			t.Error("missing byte-cap truncation message")
		}
		if !strings.Contains(out, "duplicate pairs truncated") {
			t.Error("missing final pairs-truncated summary")
		}
	})

	t.Run("stops at the 500-pair cap", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, nil))

		logDuplicateGroups(log, makeDupReport(1, 600, 2))

		out := buf.String()
		if got := strings.Count(out, dupFileMsg); got != 500 {
			t.Errorf("emitted %d duplicate-file lines, want 500 (pair cap)", got)
		}
		if strings.Contains(out, "byte cap reached") {
			t.Error("byte cap fired unexpectedly for tiny paths")
		}
		if !strings.Contains(out, "duplicate pairs truncated") {
			t.Error("missing final pairs-truncated summary")
		}
	})

	t.Run("reports document totals when the decoder capped retention", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, nil))

		// Model a retention-capped decode: maxLoggedGroups retained groups,
		// but a 150-group document. Every retained pair is emitted and the
		// truncation summary reports the full-document totals.
		report := makeDupReport(maxLoggedGroups, 1, 2)
		report.TotalGroups = 150
		report.TotalDuplicates = 150
		logDuplicateGroups(log, report)

		out := buf.String()
		if got := strings.Count(out, dupFileMsg); got != maxLoggedGroups {
			t.Errorf("emitted %d duplicate-file lines, want %d (retained groups)", got, maxLoggedGroups)
		}
		if !strings.Contains(out, "duplicate pairs truncated") {
			t.Error("missing final pairs-truncated summary")
		}
		if !strings.Contains(out, "total_pairs=150") || !strings.Contains(out, "total_groups=150") {
			t.Errorf("truncation summary must carry the document totals, got %q", out)
		}
	})

	t.Run("logs the pair whose cumulative detail bytes equal the cap exactly", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, nil))

		// A single (keeper, duplicate) pair whose lengths sum to exactly
		// logDetailCapBytes. The byte cap is exclusive: a pair landing right on
		// it is still emitted and no truncation fires. An inclusive cap would
		// drop this pair and log "byte cap reached" instead.
		keeper := strings.Repeat("k", 10)
		dup := strings.Repeat("d", logDetailCapBytes-10)
		report := parsing.Report{
			Groups:          []parsing.DuplicateGroup{{Keeper: keeper, SizePerDup: "1 KB", Duplicates: []string{dup}}},
			TotalGroups:     1,
			TotalDuplicates: 1,
		}
		logDuplicateGroups(log, &report)

		out := buf.String()
		if got := strings.Count(out, dupFileMsg); got != 1 {
			t.Errorf("emitted %d duplicate-file lines, want 1 (a pair whose detail bytes equal the cap is logged; the cap is exclusive)", got)
		}
		if strings.Contains(out, "byte cap reached") {
			t.Errorf("byte-cap truncation fired when detail bytes only equalled the cap; the cap must be exclusive, got %q", out)
		}
	})

	t.Run("numbers groups one-based in the duplicate-file lines", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, nil))

		report := parsing.Report{
			Groups: []parsing.DuplicateGroup{
				{Keeper: "/a/keeper", SizePerDup: "1 KB", Duplicates: []string{"/a/dup"}},
				{Keeper: "/b/keeper", SizePerDup: "1 KB", Duplicates: []string{"/b/dup"}},
			},
			TotalGroups:     2,
			TotalDuplicates: 2,
		}
		logDuplicateGroups(log, &report)

		// Groups are displayed one-based: group index i is rendered as group
		// i+1, so the first group is group=1 and the second group=2. A shifted
		// numbering (i-1, i, or i%1) would render group=-1/group=0 instead.
		out := buf.String()
		if !strings.Contains(out, "group=1") {
			t.Errorf("first group not labelled group=1 (one-based display), got %q", out)
		}
		if !strings.Contains(out, "group=2") {
			t.Errorf("second group not labelled group=2 (one-based display), got %q", out)
		}
	})
}

func TestShouldRunAction(t *testing.T) {
	t.Parallel()

	t.Run("skips and logs when no duplicates and action is not group", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, nil))

		got := shouldRunAction(log, &config{Action: actionLink}, false)

		if got {
			t.Error("shouldRunAction(dups=false, action=link) = true, want false")
		}
		if !strings.Contains(buf.String(), `msg="action skipped"`) || !strings.Contains(buf.String(), "reason=no_duplicates") {
			t.Errorf("expected 'action skipped' log with reason=no_duplicates, got %q", buf.String())
		}
	})

	t.Run("skips silently for the group action regardless of duplicates", func(t *testing.T) {
		t.Parallel()
		for _, dups := range []bool{false, true} {
			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, nil))

			got := shouldRunAction(log, &config{Action: actionGroup}, dups)

			if got {
				t.Errorf("shouldRunAction(dups=%v, action=group) = true, want false (report-only has no action)", dups)
			}
			if buf.Len() != 0 {
				t.Errorf("group action must skip silently, got %q", buf.String())
			}
		}
	})

	t.Run("runs silently when duplicates were found", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, nil))

		got := shouldRunAction(log, &config{Action: actionLink}, true)

		if !got {
			t.Error("shouldRunAction(dups=true, action=link) = false, want true")
		}
		if buf.Len() != 0 {
			t.Errorf("expected no log when duplicates found, got %q", buf.String())
		}
	})
}

func TestPhaseContext(t *testing.T) {
	t.Parallel()

	t.Run("positive timeout sets a deadline near now+timeout", func(t *testing.T) {
		t.Parallel()
		start := time.Now()
		ctx, cancel := phaseContext(t.Context(), 30*time.Second)
		defer cancel()

		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("phaseContext(bg, 30s): no deadline set, want a deadline")
		}
		if d := deadline.Sub(start); d < 29*time.Second || d > 31*time.Second {
			t.Errorf("phaseContext(bg, 30s) deadline in %s, want ~30s from start", d)
		}
	})

	t.Run("zero timeout sets no deadline", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := phaseContext(t.Context(), 0)
		defer cancel()

		if deadline, ok := ctx.Deadline(); ok {
			t.Errorf("phaseContext(bg, 0) deadline = %v, want no deadline (0 = unbounded phase)", deadline)
		}
	})

	t.Run("negative timeout sets no deadline", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := phaseContext(t.Context(), -1*time.Hour)
		defer cancel()

		if deadline, ok := ctx.Deadline(); ok {
			t.Errorf("phaseContext(bg, -1h) deadline = %v, want no deadline (non-positive = unbounded)", deadline)
		}
	})

	t.Run("zero timeout still cancels when the parent is cancelled", func(t *testing.T) {
		t.Parallel()
		parent, cancelParent := context.WithCancel(t.Context())
		ctx, cancel := phaseContext(parent, 0)
		defer cancel()

		select {
		case <-ctx.Done():
			t.Fatal("phaseContext child Done before parent cancelled")
		default:
		}

		cancelParent()

		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
			t.Error("phaseContext(parent, 0) child not cancelled after parent cancel; SIGTERM would not stop an unbounded phase")
		}
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Errorf("ctx.Err() = %v, want context.Canceled", ctx.Err())
		}
	})
}

// --- Tests: sweepStaleReports ---

func TestSweepStaleReports(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Two files matching reportTempPattern, created exactly as runFclonesJob
	// creates them, plus one non-matching file that must survive the sweep.
	var stale []string
	for range 2 {
		f, err := os.CreateTemp(dir, reportTempPattern)
		if err != nil {
			t.Fatalf("CreateTemp: %v", err)
		}
		_ = f.Close()
		stale = append(stale, f.Name())
	}
	keep := filepath.Join(dir, "keep.dat")
	if err := os.WriteFile(keep, []byte("state"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	sweepStaleReports(dir)

	for _, p := range stale {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("stale report %q still present after sweep (err=%v), want removed", p, err)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("non-matching file %q removed by sweep (err=%v), want kept", keep, err)
	}
}

func TestSweepStaleReportsContinuesPastUnremovable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// The unremovable entry sorts BEFORE the removable one (filepath.Glob
	// returns sorted matches), so the loop must survive the os.Remove failure
	// to reach and delete the removable file. A non-empty directory whose name
	// matches the glob makes os.Remove fail with ENOTEMPTY.
	blocked := filepath.Join(dir, "fclones_report_aaa.txt")
	if err := os.Mkdir(blocked, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(blocked, "child"), []byte("y"), 0o644); err != nil {
		t.Fatalf("WriteFile child: %v", err)
	}
	removable := filepath.Join(dir, "fclones_report_bbb.txt")
	if err := os.WriteFile(removable, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	sweepStaleReports(dir)

	if _, err := os.Stat(removable); !os.IsNotExist(err) {
		t.Errorf("removable match still present after sweep (err=%v); the unremovable sibling aborted the loop", err)
	}
	if _, err := os.Stat(blocked); err != nil {
		t.Errorf("unremovable directory %q gone (err=%v), want it left in place", blocked, err)
	}
}

func TestClassifyAndLogOutcome(t *testing.T) {
	t.Parallel()

	// Both helpers stay on context.Background(): they are deliberately
	// pre-cancelled/pre-expired to drive the shutdown and timeout arms.
	cancelledCtx := func() context.Context {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx
	}
	deadlineCtx := func() context.Context {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		return ctx
	}

	t.Run("success returns not-done with no error and logs nothing", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, nil))

		done, err := classifyAndLogOutcome(t.Context(), t.Context(), log,
			"scan", nil, time.Minute, time.Second,
			&ioutil.LimitedBuffer{Max: 1024}, nil)

		if done {
			t.Error("classifyAndLogOutcome(success) done = true, want false")
		}
		if err != nil {
			t.Errorf("classifyAndLogOutcome(success) err = %v, want nil", err)
		}
		if buf.Len() != 0 {
			t.Errorf("classifyAndLogOutcome(success) logged %q, want no output", buf.String())
		}
	})

	t.Run("shutdown returns done with nil error and logs interrupted", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, nil))

		done, err := classifyAndLogOutcome(cancelledCtx(), t.Context(), log,
			"scan", context.Canceled, time.Minute, time.Second,
			&ioutil.LimitedBuffer{Max: 1024}, nil)

		if !done {
			t.Error("classifyAndLogOutcome(shutdown) done = false, want true")
		}
		if err != nil {
			t.Errorf("classifyAndLogOutcome(shutdown) err = %v, want nil (expected shutdown is not an error)", err)
		}
		if !strings.Contains(buf.String(), `msg="scan interrupted"`) {
			t.Errorf("classifyAndLogOutcome(shutdown) log = %q, want it to contain 'scan interrupted'", buf.String())
		}
		if !strings.Contains(buf.String(), "outcome=shutdown") {
			t.Errorf("classifyAndLogOutcome(shutdown) log = %q, want the outcome=shutdown attr", buf.String())
		}
	})

	t.Run("timeout returns done with a timeout error and logs stderr", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, nil))
		stderr := &ioutil.LimitedBuffer{Max: 1024}
		_, _ = stderr.Write([]byte("boom"))

		done, err := classifyAndLogOutcome(t.Context(), deadlineCtx(), log,
			"scan", context.DeadlineExceeded, 30*time.Second, 31*time.Second,
			stderr, nil)

		if !done {
			t.Error("classifyAndLogOutcome(timeout) done = false, want true")
		}
		if err == nil || !strings.Contains(err.Error(), "scan timeout exceeded after 30s") {
			t.Errorf("classifyAndLogOutcome(timeout) err = %v, want 'scan timeout exceeded after 30s'", err)
		}
		out := buf.String()
		if !strings.Contains(out, `msg="scan timeout exceeded"`) || !strings.Contains(out, "stderr=boom") {
			t.Errorf("classifyAndLogOutcome(timeout) log = %q, want the timeout message and captured stderr", out)
		}
		if !strings.Contains(out, "outcome=timeout") {
			t.Errorf("classifyAndLogOutcome(timeout) log = %q, want the outcome=timeout attr", out)
		}
	})

	t.Run("exec error returns done with an exec error and omits stdout when nil", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, nil))
		stderr := &ioutil.LimitedBuffer{Max: 1024}
		_, _ = stderr.Write([]byte("permission denied"))

		done, err := classifyAndLogOutcome(t.Context(), t.Context(), log,
			"scan", errors.New("exit status 1"), time.Minute, 2*time.Second,
			stderr, nil)

		if !done {
			t.Error("classifyAndLogOutcome(exec_error) done = false, want true")
		}
		if err == nil || !strings.Contains(err.Error(), "scan exec failed") {
			t.Errorf("classifyAndLogOutcome(exec_error) err = %v, want 'scan exec failed'", err)
		}
		out := buf.String()
		if !strings.Contains(out, `msg="scan failed"`) || !strings.Contains(out, "stderr=\"permission denied\"") {
			t.Errorf("classifyAndLogOutcome(exec_error) log = %q, want failure message and stderr", out)
		}
		if !strings.Contains(out, "outcome=exec_error") {
			t.Errorf("classifyAndLogOutcome(exec_error) log = %q, want the outcome=exec_error attr", out)
		}
		if strings.Contains(out, "stdout=") {
			t.Errorf("classifyAndLogOutcome(exec_error, stdout=nil) log = %q, must not emit a stdout attr", out)
		}
	})

	t.Run("exec error logs stdout and extra attrs for the action phase", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, nil))
		stderr := &ioutil.LimitedBuffer{Max: 1024}
		stdout := &ioutil.LimitedBuffer{Max: 1024}
		_, _ = stdout.Write([]byte("actionout"))

		done, err := classifyAndLogOutcome(t.Context(), t.Context(), log,
			"action", errors.New("exit status 1"), time.Minute, time.Second,
			stderr, stdout, "action", "link")

		if !done || err == nil {
			t.Fatalf("classifyAndLogOutcome(action exec_error) = (%v, %v), want (true, error)", done, err)
		}
		out := buf.String()
		if !strings.Contains(out, "stdout=actionout") {
			t.Errorf("classifyAndLogOutcome(action, stdout set) log = %q, want a stdout attr", out)
		}
		if !strings.Contains(out, "action=link") {
			t.Errorf("classifyAndLogOutcome(action) log = %q, want the extra action=link attr", out)
		}
	})
}
