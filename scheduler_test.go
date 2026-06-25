package main

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/fclones-wrapper/internal/parsing"
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
			want:    []string{"group", "/data", "--min-size", "1024", "--cache"},
			wantLen: 5,
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
			wantLen: 3,
		},
		{
			name:    "extra args",
			cfg:     config{ScanPath: "/data", Args: "--min-size 1M --threads 4"},
			wantLen: 7,
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
			want:    []string{"group", "/data", "/media", "/backup", "--cache"},
			wantLen: 5,
		},
	}

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
			if got[len(got)-1] != "--cache" {
				t.Errorf("last arg = %q, want \"--cache\"", got[len(got)-1])
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

// --- Tests: scan overlap lock ---

func TestFileLockMutualExclusion(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "scan.lock")

	first, ok, err := tryLock(path)
	if err != nil {
		t.Fatalf("first tryLock: unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("first tryLock should acquire the lock")
	}

	if _, ok, err := tryLock(path); err != nil {
		t.Fatalf("second tryLock: unexpected error: %v", err)
	} else if ok {
		t.Error("second tryLock should fail while the lock is held")
	}

	first.unlock()

	again, ok, err := tryLock(path)
	if err != nil {
		t.Fatalf("third tryLock: unexpected error: %v", err)
	}
	if !ok {
		t.Error("tryLock should re-acquire after unlock")
	}
	again.unlock()
}

func TestTryLockReturnsErrorWhenLockFileCannotBeCreated(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "missing-parent", "scan.lock")

	l, ok, err := tryLock(path)

	if err == nil {
		t.Fatalf("tryLock(%q) error = nil, want a non-nil error when the parent directory does not exist", path)
	}
	if ok {
		t.Errorf("tryLock(%q) ok = true, want false on open failure", path)
	}
	if l != nil {
		t.Errorf("tryLock(%q) lock = %v, want nil on open failure", path, l)
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

// --- Tests: countDuplicateFiles ---

func TestCountDuplicateFiles(t *testing.T) {
	t.Parallel()
	groups := []parsing.DuplicateGroup{
		{Keeper: "/a", Duplicates: []string{"/b", "/c"}},
		{Keeper: "/d", Duplicates: []string{"/e"}},
	}
	if got := countDuplicateFiles(groups); got != 3 {
		t.Errorf("countDuplicateFiles = %d, want 3", got)
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
		if got[len(got)-1] != "--cache" {
			rt.Fatalf("buildScanArgs: last arg = %q, want \"--cache\"", got[len(got)-1])
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
	cmd := defaultCommandRunner(context.Background(), "fclones", "group", "/scandir")
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

func makeDupGroups(n, dupsPerGroup, pathLen int) []parsing.DuplicateGroup {
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
	return groups
}

func TestLogDuplicateGroups(t *testing.T) {
	t.Parallel()

	const dupFileMsg = `msg="duplicate file"`

	t.Run("emits every pair when under all caps", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, nil))

		groups := makeDupGroups(3, 2, 8)
		logDuplicateGroups(log, groups)

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

		groups := makeDupGroups(1, 100, 1024)
		logDuplicateGroups(log, groups)

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

		groups := makeDupGroups(1, 600, 2)
		logDuplicateGroups(log, groups)

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

	t.Run("stops at the 100-group cap", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, nil))

		groups := makeDupGroups(150, 1, 2)
		logDuplicateGroups(log, groups)

		out := buf.String()
		if got := strings.Count(out, dupFileMsg); got != 100 {
			t.Errorf("emitted %d duplicate-file lines, want 100 (group cap)", got)
		}
		if !strings.Contains(out, "duplicate pairs truncated") {
			t.Error("missing final pairs-truncated summary")
		}
	})
}

func TestMaybeWarnGroupCountDrift(t *testing.T) {
	t.Parallel()

	const driftMsg = "group count mismatch, possible fclones format drift"

	t.Run("warns when reported and parsed counts differ", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

		maybeWarnGroupCountDrift(log, true, "5", 3)

		out := buf.String()
		if !strings.Contains(out, driftMsg) {
			t.Errorf("maybeWarnGroupCountDrift(true, %q, 3): no drift warning, got %q", "5", out)
		}
		if !strings.Contains(out, "reported_groups=5") || !strings.Contains(out, "parsed_groups=3") {
			t.Errorf("maybeWarnGroupCountDrift(true, %q, 3): warning missing counts, got %q", "5", out)
		}
	})

	t.Run("silent when counts match", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

		maybeWarnGroupCountDrift(log, true, "7", 7)

		if out := buf.String(); out != "" {
			t.Errorf("maybeWarnGroupCountDrift(true, %q, 7) = %q, want no log", "7", out)
		}
	})

	t.Run("silent when report was not parsed even on mismatch", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

		maybeWarnGroupCountDrift(log, false, "5", 3)

		if out := buf.String(); out != "" {
			t.Errorf("maybeWarnGroupCountDrift(false, %q, 3) = %q, want no log", "5", out)
		}
	})

	t.Run("silent when reported count is non-numeric", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

		maybeWarnGroupCountDrift(log, true, "not-a-number", 3)

		if out := buf.String(); out != "" {
			t.Errorf("maybeWarnGroupCountDrift(true, %q, 3) = %q, want no log", "not-a-number", out)
		}
	})
}

func TestShouldRunAction(t *testing.T) {
	t.Parallel()

	t.Run("skips and logs when parsed report has no duplicates and action is not group", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, nil))

		got := shouldRunAction(log, &config{Action: actionLink}, true, false)

		if got {
			t.Error("shouldRunAction(parsed=true, dups=false, action=link) = true, want false")
		}
		if !strings.Contains(buf.String(), `msg="action skipped"`) {
			t.Errorf("expected 'action skipped' log, got %q", buf.String())
		}
	})

	t.Run("skips silently when no duplicates and action is group", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, nil))

		got := shouldRunAction(log, &config{Action: actionGroup}, true, false)

		if got {
			t.Error("shouldRunAction(parsed=true, dups=false, action=group) = true, want false")
		}
		if strings.Contains(buf.String(), "action skipped") {
			t.Errorf("group action with no dups should not log 'action skipped', got %q", buf.String())
		}
	})

	t.Run("runs silently when duplicates were found", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, nil))

		got := shouldRunAction(log, &config{Action: actionLink}, true, true)

		if !got {
			t.Error("shouldRunAction(parsed=true, dups=true, action=link) = false, want true")
		}
		if buf.Len() != 0 {
			t.Errorf("expected no log when duplicates found, got %q", buf.String())
		}
	})

	t.Run("runs and warns when report unparseable and action is not group", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, nil))

		got := shouldRunAction(log, &config{Action: actionRemove}, false, false)

		if !got {
			t.Error("shouldRunAction(parsed=false, action=remove) = false, want true")
		}
		if !strings.Contains(buf.String(), "running action without parsed report") {
			t.Errorf("expected degraded-run warning, got %q", buf.String())
		}
	})

	t.Run("runs silently when report unparseable and action is group", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, nil))

		got := shouldRunAction(log, &config{Action: actionGroup}, false, false)

		if !got {
			t.Error("shouldRunAction(parsed=false, action=group) = false, want true")
		}
		if strings.Contains(buf.String(), "running action without parsed report") {
			t.Errorf("group action should not warn about unparseable report, got %q", buf.String())
		}
	})
}
