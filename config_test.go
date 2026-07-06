package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"
)

// setCleanFclonesEnv neutralises every FCLONES_* env var that loadConfig
// reads. Without this, an inherited env from a developer's shell or CI
// runner can crash an in-process loadConfig test.
func setCleanFclonesEnv(t *testing.T) {
	t.Helper()
	t.Setenv("FCLONES_INTERVAL", "3h")
	t.Setenv("FCLONES_SCAN_PATHS", scanDir)
	t.Setenv("FCLONES_ARGS", "")
	t.Setenv("FCLONES_ACTION", string(actionGroup))
	t.Setenv("FCLONES_ACTION_ARGS", "")
	t.Setenv("FCLONES_ALLOW_UNSAFE", "")
	t.Setenv("FCLONES_SCAN_TIMEOUT", "12h")
}

func TestLoadConfig(t *testing.T) {
	setCleanFclonesEnv(t)
	t.Setenv("FCLONES_INTERVAL", "1h")
	t.Setenv("FCLONES_SCAN_PATHS", "/data")
	t.Setenv("FCLONES_ARGS", "--min-size 1024")
	t.Setenv("FCLONES_ACTION", "dedupe")
	t.Setenv("FCLONES_ACTION_ARGS", "--rf-over 1")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	if cfg.Interval != time.Hour {
		t.Errorf("Interval = %s, want 1h", cfg.Interval)
	}
	if cfg.Mode != modeBuiltin {
		t.Errorf("Mode = %s, want built-in for a duration interval", cfg.Mode)
	}
	if cfg.ScanPath != "/data" {
		t.Errorf("ScanPath = %q, want %q", cfg.ScanPath, "/data")
	}
	if cfg.Args != "--min-size 1024" {
		t.Errorf("Args = %q, want %q", cfg.Args, "--min-size 1024")
	}
	if cfg.Action != actionDedupe {
		t.Errorf("Action = %q, want %q", cfg.Action, actionDedupe)
	}
	if cfg.ActionArgs != "--rf-over 1" {
		t.Errorf("ActionArgs = %q, want %q", cfg.ActionArgs, "--rf-over 1")
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	setCleanFclonesEnv(t)
	// Override interval to empty to exercise the default path.
	t.Setenv("FCLONES_INTERVAL", "")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	if cfg.Interval != defaultInterval {
		t.Errorf("Interval = %s, want default %s", cfg.Interval, defaultInterval)
	}
	if cfg.Mode != modeBuiltin {
		t.Errorf("Mode = %s, want built-in when interval is unset (default cadence)", cfg.Mode)
	}
	if cfg.ScanPath != scanDir {
		t.Errorf("ScanPath = %q, want %q", cfg.ScanPath, scanDir)
	}
	if cfg.Action != actionGroup {
		t.Errorf("Action = %q, want %q", cfg.Action, actionGroup)
	}
	if cfg.Args != "" {
		t.Errorf("Args = %q, want empty", cfg.Args)
	}
}

func TestLoadConfigScheduleDisabled(t *testing.T) {
	for _, value := range []string{"off", "OFF", "disabled", "Disabled"} {
		t.Run(value, func(t *testing.T) {
			setCleanFclonesEnv(t)
			t.Setenv("FCLONES_INTERVAL", value)

			cfg, err := loadConfig()
			if err != nil {
				t.Fatalf("loadConfig: %v", err)
			}
			if cfg.Mode != modeExternal {
				t.Errorf("FCLONES_INTERVAL=%q: Mode = %s, want external", value, cfg.Mode)
			}
		})
	}
}

func TestLoadConfigRunOnce(t *testing.T) {
	// A zero duration runs exactly one scan, then exits -- distinct from the
	// off/disabled idle mode.
	for _, value := range []string{"0", "0s", "0h"} {
		t.Run(value, func(t *testing.T) {
			setCleanFclonesEnv(t)
			t.Setenv("FCLONES_INTERVAL", value)

			cfg, err := loadConfig()
			if err != nil {
				t.Fatalf("loadConfig: %v", err)
			}
			if cfg.Mode != modeOnce {
				t.Errorf("FCLONES_INTERVAL=%q: Mode = %s, want once", value, cfg.Mode)
			}
		})
	}
}

func TestLoadConfigNegativeIntervalFallsBackToDefault(t *testing.T) {
	// A negative duration is a likely typo; it must NOT disable scanning. It
	// falls back to the default cadence in built-in mode so the container keeps
	// reclaiming space rather than silently idling while reporting healthy.
	for _, value := range []string{"-1h", "-30m", "-1s"} {
		t.Run(value, func(t *testing.T) {
			setCleanFclonesEnv(t)
			t.Setenv("FCLONES_INTERVAL", value)

			cfg, err := loadConfig()
			if err != nil {
				t.Fatalf("loadConfig with FCLONES_INTERVAL=%q: unexpected error: %v", value, err)
			}
			if cfg.Mode != modeBuiltin {
				t.Errorf("FCLONES_INTERVAL=%q: Mode = %s, want built-in", value, cfg.Mode)
			}
			if cfg.Interval != defaultInterval {
				t.Errorf("FCLONES_INTERVAL=%q: Interval = %s, want default %s", value, cfg.Interval, defaultInterval)
			}
		})
	}
}

func TestLoadConfigGarbageIntervalFallsBackToDefault(t *testing.T) {
	// A non-sentinel unparseable value must NOT disable scheduling; it
	// falls back to the default cadence so the container keeps scanning.
	setCleanFclonesEnv(t)
	t.Setenv("FCLONES_INTERVAL", "not-a-duration")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Mode != modeBuiltin {
		t.Errorf("Mode = %s, want built-in on unparseable non-sentinel value", cfg.Mode)
	}
	if cfg.Interval != defaultInterval {
		t.Errorf("Interval = %s, want default %s", cfg.Interval, defaultInterval)
	}
}

func TestLoadConfigAllowUnsafe(t *testing.T) {
	setCleanFclonesEnv(t)
	t.Setenv("FCLONES_ARGS", "--transform /usr/bin/strip")
	t.Setenv("FCLONES_ALLOW_UNSAFE", "true")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	if cfg.Args != "--transform /usr/bin/strip" {
		t.Errorf("Args = %q, want --transform flag to be accepted with ALLOW_UNSAFE", cfg.Args)
	}
}

func TestLoadConfigAllowUnsafeCaseInsensitive(t *testing.T) {
	setCleanFclonesEnv(t)
	t.Setenv("FCLONES_ACTION_ARGS", "--command echo hello")
	t.Setenv("FCLONES_ALLOW_UNSAFE", "TRUE")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	if cfg.ActionArgs != "--command echo hello" {
		t.Errorf("ActionArgs = %q, want --command flag accepted with ALLOW_UNSAFE=TRUE", cfg.ActionArgs)
	}
}

func TestRejectDangerousArgs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		input     string
		dangerous bool
	}{
		{"safe flags", "--min-size 1024 --threads 4", false},
		{"empty string", "", false},
		{"--command bare", "--command rm -rf /", true},
		{"--command=value", "--command=evil", true},
		{"case insensitive", "--COMMAND=evil", true},
		{"mixed case", "--Command something", true},
		{"buried in middle", "--min-size 1024 --command evil --threads 4", true},
		{"similar prefix is safe", "--commander 5", false},
		{"no dashes is safe", "command evil", false},
		{"--transform bare", "--transform /usr/bin/evil", true},
		{"--transform=value", "--transform=/usr/bin/evil", true},
		{"--transform case insensitive", "--TRANSFORM=evil", true},
		{"--in-place bare", "--in-place", true},
		{"--in-place=value", "--IN-PLACE=true", true},
		{"--no-copy bare", "--no-copy", true},
		{"--no-copy=value", "--NO-COPY=true", true},
		{"--transformer is safe", "--transformer 5", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := rejectDangerousArgs(tt.input, "TEST_ENV")
			if (err != nil) != tt.dangerous {
				t.Errorf("rejectDangerousArgs(%q) error = %v, want dangerous=%v", tt.input, err, tt.dangerous)
			}
			if tt.dangerous && err != nil && !strings.Contains(err.Error(), "dangerous flag") {
				t.Errorf("rejectDangerousArgs(%q) error = %q, want it to mention 'dangerous flag'", tt.input, err)
			}
		})
	}
}

// --- Tests: verifyDir ---

func TestVerifyDirHappyPath(t *testing.T) {
	t.Parallel()
	if err := verifyDir(context.Background(), t.TempDir(), 5*time.Second); err != nil {
		t.Errorf("verifyDir on writable temp dir: unexpected error: %v", err)
	}
}

func TestVerifyDirCreatesMissingSubpath(t *testing.T) {
	t.Parallel()
	subdir := filepath.Join(t.TempDir(), "a", "b", "c")
	if err := verifyDir(context.Background(), subdir, 5*time.Second); err != nil {
		t.Errorf("verifyDir creating nested dir: unexpected error: %v", err)
	}
	if _, err := os.Stat(subdir); err != nil {
		t.Errorf("expected %s to exist, got stat error: %v", subdir, err)
	}
}

func TestVerifyDirReadOnly(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("root bypasses file mode permissions")
	}
	parent := t.TempDir()
	dir := filepath.Join(parent, "readonly")
	if err := os.MkdirAll(dir, 0o500); err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer func() { _ = os.Chmod(dir, 0o700) }()

	err := verifyDir(context.Background(), dir, 5*time.Second)
	if err == nil {
		t.Error("expected verifyDir on read-only dir to return an error")
	}
}

func TestVerifyDirCreateProbeFileFails(t *testing.T) {
	t.Parallel()
	// Root-safe cover of writeProbe's os.Create error branch. The sibling
	// TestVerifyDirReadOnly reaches it only via an EACCES denial, which
	// os.Getuid()==0 skips -- and the suite runs as root in CI -- so under
	// root the create-failure path is otherwise untested. A directory
	// pre-created at the probe's ".write_test" path makes os.Create fail
	// with EISDIR for any UID (unlike a permission denial), covering the
	// branch with no root skip.
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".write_test"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	err := verifyDir(context.Background(), dir, 5*time.Second)

	if err == nil {
		t.Fatal("verifyDir with a directory occupying the .write_test probe path: error = nil, want an os.Create (EISDIR) failure")
	}
	if !strings.Contains(err.Error(), "create") {
		t.Errorf("verifyDir error = %q, want it to mention 'create' (the writeProbe os.Create-branch wrap)", err)
	}
}

// --- Tests: getEnv ---

func TestGetEnv(t *testing.T) {
	t.Setenv("TEST_FCLONES_ENV", "value")
	if got := getEnv("TEST_FCLONES_ENV", "default"); got != "value" {
		t.Errorf("getEnv = %q, want value", got)
	}
	t.Setenv("TEST_FCLONES_ENV", "")
	if got := getEnv("TEST_FCLONES_ENV", "default"); got != "default" {
		t.Errorf("getEnv = %q, want default", got)
	}
}

func TestGetEnvUnset(t *testing.T) {
	t.Parallel()
	got := getEnv("FCLONES_TEST_UNSET_VAR_12345", "fallback")
	if got != "fallback" {
		t.Errorf("getEnv unset = %q, want \"fallback\"", got)
	}
}

func TestGetEnvEmptyFallback(t *testing.T) {
	t.Parallel()
	got := getEnv("FCLONES_TEST_UNSET_VAR_12345", "")
	if got != "" {
		t.Errorf("getEnv empty fallback = %q, want \"\"", got)
	}
}

// --- Tests: loadConfig error paths (direct, no subprocess needed) ---

func TestLoadConfigErrorOnInvalidAction(t *testing.T) {
	setCleanFclonesEnv(t)
	t.Setenv("FCLONES_ACTION", "shell")

	_, err := loadConfig()
	if err == nil {
		t.Fatal("expected error for invalid action")
	}
	if !strings.Contains(err.Error(), "invalid action") {
		t.Errorf("error = %q, want to contain 'invalid action'", err)
	}
}

func TestLoadConfigErrorOnDangerousArgs(t *testing.T) {
	tests := []struct {
		name   string
		envVar string
		value  string
	}{
		{"--command in FCLONES_ARGS", "FCLONES_ARGS", "--command evil"},
		{"--transform in FCLONES_ARGS", "FCLONES_ARGS", "--transform=/bin/sh"},
		{"--in-place in FCLONES_ARGS", "FCLONES_ARGS", "--in-place"},
		{"--no-copy in FCLONES_ARGS", "FCLONES_ARGS", "--no-copy=true"},
		{"--command in FCLONES_ACTION_ARGS", "FCLONES_ACTION_ARGS", "--command rm"},
		{"case-insensitive --COMMAND", "FCLONES_ARGS", "--COMMAND=evil"},
		{"flag buried mid-args", "FCLONES_ARGS", "--min-size 1024 --command x --threads 4"},
		{"--transform in FCLONES_SCAN_PATHS", "FCLONES_SCAN_PATHS", "/scandir --transform /evil"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setCleanFclonesEnv(t)
			t.Setenv(tt.envVar, tt.value)

			_, err := loadConfig()
			if err == nil {
				t.Fatalf("loadConfig(%s=%q) should return error", tt.envVar, tt.value)
			}
			if !strings.Contains(err.Error(), "dangerous flag") {
				t.Errorf("error = %q, want to contain 'dangerous flag'", err)
			}
		})
	}
}

func TestLoadConfigErrorOnUnterminatedQuote(t *testing.T) {
	setCleanFclonesEnv(t)
	t.Setenv("FCLONES_ARGS", `--path "/unterminated`)

	_, err := loadConfig()
	if err == nil {
		t.Fatal("expected error for unterminated quote")
	}
	if !strings.Contains(err.Error(), "invalid argument syntax") {
		t.Errorf("error = %q, want to contain 'invalid argument syntax'", err)
	}
}

func TestLoadConfigAllowUnsafeBypassesGuardrails(t *testing.T) {
	setCleanFclonesEnv(t)
	t.Setenv("FCLONES_ARGS", "--transform /usr/bin/strip")
	t.Setenv("FCLONES_ALLOW_UNSAFE", "true")

	_, err := loadConfig()
	if err != nil {
		t.Errorf("loadConfig with ALLOW_UNSAFE=true should succeed, got: %v", err)
	}
}

func TestLoadConfigAllowUnsafeRejectsInvalidArgSyntax(t *testing.T) {
	tests := []struct {
		name   string
		envVar string
		value  string
	}{
		{"unterminated quote in FCLONES_ARGS", "FCLONES_ARGS", `--path "/unterminated`},
		{"unterminated quote in FCLONES_ACTION_ARGS", "FCLONES_ACTION_ARGS", `--exclude 'half`},
		{"trailing backslash in FCLONES_SCAN_PATHS", "FCLONES_SCAN_PATHS", `/scandir\`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setCleanFclonesEnv(t)
			t.Setenv("FCLONES_ALLOW_UNSAFE", "true")
			t.Setenv(tt.envVar, tt.value)

			_, err := loadConfig()
			if err == nil {
				t.Fatalf("loadConfig(ALLOW_UNSAFE=true, %s=%q) error = nil, want a syntax error", tt.envVar, tt.value)
			}
			if !strings.Contains(err.Error(), "invalid argument syntax") {
				t.Errorf("error = %q, want it to contain 'invalid argument syntax'", err)
			}
			if !strings.Contains(err.Error(), tt.envVar) {
				t.Errorf("error = %q, want it to name the offending env var %q", err, tt.envVar)
			}
		})
	}
}

func TestLoadConfigErrorOnInvalidScanTimeout(t *testing.T) {
	setCleanFclonesEnv(t)
	t.Setenv("FCLONES_SCAN_TIMEOUT", "not-a-duration")

	_, err := loadConfig()
	if err == nil {
		t.Fatal("expected error for invalid scan timeout")
	}
	if !strings.Contains(err.Error(), "invalid FCLONES_SCAN_TIMEOUT") {
		t.Errorf("error = %q, want to contain 'invalid FCLONES_SCAN_TIMEOUT'", err)
	}
}

func TestLoadConfigScanTimeoutNonPositive(t *testing.T) {
	t.Run("zero disables the per-phase timeout", func(t *testing.T) {
		for _, v := range []string{"0", "0s"} {
			setCleanFclonesEnv(t)
			t.Setenv("FCLONES_SCAN_TIMEOUT", v)

			cfg, err := loadConfig()
			if err != nil {
				t.Fatalf("loadConfig with FCLONES_SCAN_TIMEOUT=%q returned %v, want nil (0 = no timeout)", v, err)
			}
			if cfg.PhaseTimeout != 0 {
				t.Errorf("PhaseTimeout = %v for %q, want 0 (no timeout)", cfg.PhaseTimeout, v)
			}
		}
	})

	t.Run("negative is rejected", func(t *testing.T) {
		setCleanFclonesEnv(t)
		t.Setenv("FCLONES_SCAN_TIMEOUT", "-1h")

		_, err := loadConfig()
		if err == nil {
			t.Fatal("expected error for negative scan timeout")
		}
		if !strings.Contains(err.Error(), "invalid FCLONES_SCAN_TIMEOUT") {
			t.Errorf("error = %q, want to contain 'invalid FCLONES_SCAN_TIMEOUT'", err)
		}
	})
}

// --- Tests: parseAction ---

func TestParseAction(t *testing.T) {
	t.Parallel()
	t.Run("valid actions", func(t *testing.T) {
		for _, tc := range []struct {
			input string
			want  action
		}{
			{"group", actionGroup},
			{"remove", actionRemove},
			{"link", actionLink},
			{"dedupe", actionDedupe},
		} {
			got, err := parseAction(tc.input)
			if err != nil {
				t.Errorf("parseAction(%q): unexpected error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("parseAction(%q) = %q, want %q", tc.input, got, tc.want)
			}
		}
	})

	t.Run("invalid actions", func(t *testing.T) {
		for _, input := range []string{"", "delete", "exec"} {
			_, err := parseAction(input)
			if err == nil {
				t.Errorf("parseAction(%q): expected error", input)
			}
			if err != nil && !strings.Contains(err.Error(), "invalid action") {
				t.Errorf("parseAction(%q) error = %q, want to contain 'invalid action'", input, err)
			}
		}
	})
}

func TestProperty_LoadConfigValidActions(t *testing.T) {
	setCleanFclonesEnv(t)
	rapid.Check(t, func(rt *rapid.T) {
		action := rapid.SampledFrom([]string{"group", "remove", "link", "dedupe"}).Draw(rt, "action")
		t.Setenv("FCLONES_ACTION", action)
		t.Setenv("FCLONES_ARGS", "")
		t.Setenv("FCLONES_ACTION_ARGS", "")
		cfg, err := loadConfig()
		if err != nil {
			rt.Fatalf("loadConfig: %v", err)
		}
		if _, err := parseAction(string(cfg.Action)); err != nil {
			rt.Fatalf("loadConfig returned disallowed action %q", cfg.Action)
		}
	})
}

func TestVerifyDirTimeoutOnCancelledContext(t *testing.T) {
	t.Parallel()
	// Block the probe so the timeout/cancel arm wins the select
	// deterministically. With the real write probe and a pre-cancelled
	// context BOTH select cases can be ready at once, which made this test
	// flaky two ways: it non-deterministically returned nil (probe won the
	// race), and the probe's MkdirAll could re-create the t.TempDir() subdir
	// after the framework cleanup ran, failing it with "directory not empty".
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	blockingProbe := func(string) error {
		<-release
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := verifyDirWithProbe(ctx, t.TempDir(), 5*time.Second, blockingProbe)
	if err == nil {
		t.Fatal("verifyDirWithProbe with cancelled context: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("verifyDirWithProbe error = %q, want it to contain \"timed out\"", err)
	}
}

func TestVerifyDirWithProbeBoundsHungProbeByTimeout(t *testing.T) {
	t.Parallel()
	// A live (non-cancelled) parent context: the function's own timeout,
	// not parent cancellation, must bound a probe that never returns (a
	// hung filesystem). The probe is released only at cleanup so its
	// goroutine does not outlive the test.
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	hungProbe := func(string) error {
		<-release
		return nil
	}

	start := time.Now()
	err := verifyDirWithProbe(context.Background(), t.TempDir(), 50*time.Millisecond, hungProbe)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("verifyDirWithProbe with a hung probe and live parent: expected a timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("verifyDirWithProbe error = %q, want it to contain \"timed out\"", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("verifyDirWithProbe took %s, want it bounded near the 50ms timeout", elapsed)
	}
}

// --- Tests: setupLogger ---

func TestSetupLoggerLevels(t *testing.T) {
	origDefault := slog.Default()
	t.Cleanup(func() { slog.SetDefault(origDefault) })
	ctx := context.Background()

	tests := []struct {
		name  string
		level string
		want  slog.Level
	}{
		{"debug", "debug", slog.LevelDebug},
		{"info", "info", slog.LevelInfo},
		{"warn", "warn", slog.LevelWarn},
		{"warning alias", "warning", slog.LevelWarn},
		{"error", "error", slog.LevelError},
		{"uppercase recognized", "ERROR", slog.LevelError},
		{"surrounding whitespace", "  info  ", slog.LevelInfo},
		{"unrecognized defaults to info", "bogus", slog.LevelInfo},
		{"empty defaults to info", "", slog.LevelInfo},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("FCLONES_LOG_LEVEL", tt.level)
			setupLogger()
			d := slog.Default()
			if !d.Enabled(ctx, tt.want) {
				t.Errorf("FCLONES_LOG_LEVEL=%q: Enabled(%v) = false, want the handler to log at its configured level", tt.level, tt.want)
			}
			if d.Enabled(ctx, tt.want-1) {
				t.Errorf("FCLONES_LOG_LEVEL=%q: Enabled(%v) = true, want levels below %v suppressed", tt.level, tt.want-1, tt.want)
			}
		})
	}
}

func TestVerifyDirParentNotADirectory(t *testing.T) {
	t.Parallel()
	// A regular file standing in for a parent path component makes the write
	// probe's os.MkdirAll fail with ENOTDIR, exercising the mkdir-error branch --
	// the symmetric counterpart to TestVerifyDirReadOnly's create-error path.
	parent := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(parent, []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: os.WriteFile: %v", err)
	}
	target := filepath.Join(parent, "child")

	err := verifyDir(context.Background(), target, 5*time.Second)

	if err == nil {
		t.Fatalf("verifyDir(%q) error = nil, want a mkdir error when a parent path component is a regular file", target)
	}
	if !strings.Contains(err.Error(), "mkdir") {
		t.Errorf("verifyDir(%q) error = %q, want it to contain \"mkdir\"", target, err)
	}
}

func TestLoadConfigWarnsOnEmptyScanPaths(t *testing.T) {
	setCleanFclonesEnv(t)
	// A whitespace-only value is non-empty (so getEnv returns it rather than
	// the default scanDir) yet parses to zero tokens, hitting the
	// "no scan targets" branch in loadConfig.
	t.Setenv("FCLONES_SCAN_PATHS", "   ")

	orig := slog.Default()
	t.Cleanup(func() { slog.SetDefault(orig) })
	var logs strings.Builder
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))

	if _, err := loadConfig(); err != nil {
		t.Fatalf("loadConfig with whitespace FCLONES_SCAN_PATHS: unexpected error: %v", err)
	}
	if !strings.Contains(logs.String(), "resolved to no scan targets") {
		t.Errorf("loadConfig did not warn that scan paths resolved to no targets; logs = %q", logs.String())
	}
}

func TestParseInterval(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		raw          string
		wantInterval time.Duration
		wantMode     runMode
	}{
		{"empty falls back to default built-in", "", defaultInterval, modeBuiltin},
		{"whitespace only falls back to default built-in", "   ", defaultInterval, modeBuiltin},
		{"off selects external with default interval", "off", defaultInterval, modeExternal},
		{"disabled alias selects external", "disabled", defaultInterval, modeExternal},
		{"uppercase OFF selects external", "OFF", defaultInterval, modeExternal},
		{"positive duration sets built-in cadence", "90m", 90 * time.Minute, modeBuiltin},
		{"zero selects run-once with default interval", "0", defaultInterval, modeOnce},
		{"zero seconds selects run-once", "0s", defaultInterval, modeOnce},
		{"negative falls back to default built-in", "-1h", defaultInterval, modeBuiltin},
		{"unparseable falls back to default built-in", "not-a-duration", defaultInterval, modeBuiltin},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotInterval, gotMode := parseInterval(tt.raw)
			if gotInterval != tt.wantInterval {
				t.Errorf("parseInterval(%q) interval = %s, want %s", tt.raw, gotInterval, tt.wantInterval)
			}
			if gotMode != tt.wantMode {
				t.Errorf("parseInterval(%q) mode = %s, want %s", tt.raw, gotMode, tt.wantMode)
			}
		})
	}
}

func TestRunModeString(t *testing.T) {
	t.Parallel()
	tests := []struct {
		want string
		mode runMode
	}{
		{"built-in", modeBuiltin},
		{"external", modeExternal},
		{"once", modeOnce},
	}
	for _, tt := range tests {
		if got := tt.mode.String(); got != tt.want {
			t.Errorf("runMode(%d).String() = %q, want %q", int(tt.mode), got, tt.want)
		}
	}
}

func TestRunModeStringPanicsOnUnknown(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Error("runMode(99).String(): expected panic on unhandled value, got none")
		}
	}()
	_ = runMode(99).String()
}

func TestLoadConfigTagsConfigErrorOutcome(t *testing.T) {
	orig := slog.Default()
	t.Cleanup(func() { slog.SetDefault(orig) })

	tests := []struct {
		name   string
		envVar string
		value  string
		unsafe bool
	}{
		{"invalid action", "FCLONES_ACTION", "shell", false},
		{"unparseable scan timeout", "FCLONES_SCAN_TIMEOUT", "not-a-duration", false},
		{"negative scan timeout", "FCLONES_SCAN_TIMEOUT", "-1h", false},
		{"dangerous flag", "FCLONES_ARGS", "--command rm", false},
		{"invalid arg syntax", "FCLONES_ARGS", "--path \"/unterminated", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setCleanFclonesEnv(t)
			if tt.unsafe {
				t.Setenv("FCLONES_ALLOW_UNSAFE", "true")
			}
			t.Setenv(tt.envVar, tt.value)

			var logs strings.Builder
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelError})))

			if _, err := loadConfig(); err == nil {
				t.Fatalf("loadConfig(%s=%q) error = nil, want a config error", tt.envVar, tt.value)
			}
			if got := logs.String(); !strings.Contains(got, "outcome=config_error") {
				t.Errorf("loadConfig(%s=%q) log = %q, want the outcome=config_error attr so the outcome=~\".+\" dashboard query catches startup config faults", tt.envVar, tt.value, got)
			}
		})
	}
}
