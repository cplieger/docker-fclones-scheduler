package main

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func TestParseStats(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		groups string
		size   string
	}{
		{
			name:   "standard output with parenthesized size",
			input:  "# Redundant: 5 files (1.2 GB)\n# Total: 10 3 groups",
			groups: "3",
			size:   "1.2 GB",
		},
		{
			name:   "size without parentheses",
			input:  "# Redundant: 512 MB\n# Total: 5 2 groups",
			groups: "2",
			size:   "512 MB",
		},
		{
			name:   "no duplicates",
			input:  "# Redundant: 0 files\n# Total: 0 0 groups",
			groups: "0",
			size:   "0 files",
		},
		{
			name:   "real output with commas and extra fields",
			input:  "# Redundant: 1,234 files (5.6 GB) in 789 groups\n# Total: 2,000 789 groups",
			groups: "789",
			size:   "5.6 GB",
		},
		{
			name:   "empty input returns defaults",
			input:  "",
			groups: "0",
			size:   "0 B",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stats := parseStats(tt.input)
			if stats.Groups != tt.groups {
				t.Errorf("Groups = %q, want %q", stats.Groups, tt.groups)
			}
			if stats.Size != tt.size {
				t.Errorf("Size = %q, want %q", stats.Size, tt.size)
			}
		})
	}
}

func TestParseDuplicatesFormatted(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "comments only",
			input: "# some comment\n",
			want:  "No duplicates found.",
		},
		{
			name:  "single group with two files",
			input: "# comment\n\n/path/to/file1\n/path/to/file2\n",
			want:  "/path/to/file1\n↳ /path/to/file2\n",
		},
		{
			name:  "multiple groups separated by blank lines",
			input: "# comment\n\n/path/a1\n/path/a2\n/path/a3\n\n/path/b1\n/path/b2\n",
			want:  "/path/a1\n↳ /path/a2\n↳ /path/a3\n/path/b1\n↳ /path/b2\n",
		},
		{
			name:  "group header line resets file counter",
			input: "# comment\n\n3a2b,1024 B,2 * 512 B:\n/path/file1\n/path/file2\n",
			want:  "/path/file1\n↳ /path/file2\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseDuplicatesFormatted(tt.input)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractProcessedLine(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "extracts Processed line from middle",
			input: "some output\nProcessed 5 files, reclaimed 1.2 GB\nmore output",
			want:  "Processed 5 files, reclaimed 1.2 GB",
		},
		{
			name:  "falls back to last non-empty line",
			input: "some output\nlast line",
			want:  "last line",
		},
		{
			name:  "empty input",
			input: "",
			want:  "(No output captured)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractProcessedLine(tt.input)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// assertArgs is a test helper that compares parseArgs output to expected values.
func assertArgs(t *testing.T, input string, want []string) {
	t.Helper()
	got := parseArgs(input)
	if len(got) != len(want) {
		t.Fatalf("parseArgs(%q): len = %d, want %d; got %v", input, len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("parseArgs(%q)[%d] = %q, want %q", input, i, got[i], want[i])
		}
	}
}

func TestParseArgs(t *testing.T) {
	t.Run("simple flags", func(t *testing.T) {
		assertArgs(t, "--min-size 1024 --threads 4",
			[]string{"--min-size", "1024", "--threads", "4"})
	})
	t.Run("double and single quotes", func(t *testing.T) {
		assertArgs(t, `--path "/some dir/with spaces" --name 'test file'`,
			[]string{"--path", "/some dir/with spaces", "--name", "test file"})
	})
	t.Run("empty string", func(t *testing.T) {
		assertArgs(t, "", nil)
	})
	t.Run("single flag pair", func(t *testing.T) {
		assertArgs(t, "--rf-over 1", []string{"--rf-over", "1"})
	})
	t.Run("escaped space", func(t *testing.T) {
		assertArgs(t, `--path /some\ dir/test --flag`,
			[]string{"--path", "/some dir/test", "--flag"})
	})
	t.Run("tab separators", func(t *testing.T) {
		assertArgs(t, "--flag1\t--flag2\t\t--flag3",
			[]string{"--flag1", "--flag2", "--flag3"})
	})
	t.Run("leading and trailing whitespace", func(t *testing.T) {
		assertArgs(t, "  --flag1  --flag2  ",
			[]string{"--flag1", "--flag2"})
	})
}

func TestGetEnvBool(t *testing.T) {
	t.Setenv("TEST_TRUE", "true")
	t.Setenv("TEST_FALSE", "false")
	t.Setenv("TEST_INVALID", "notabool")

	if !getEnvBool("TEST_TRUE", false) {
		t.Error("expected true for TEST_TRUE")
	}
	if getEnvBool("TEST_FALSE", true) {
		t.Error("expected false for TEST_FALSE")
	}
	if !getEnvBool("TEST_INVALID", true) {
		t.Error("expected fallback true for invalid")
	}
	if getEnvBool("TEST_MISSING", false) {
		t.Error("expected fallback false for missing")
	}
}

func TestLoadConfig(t *testing.T) {
	t.Setenv("FCLONES_SCHEDULE", "0 0 * * *")
	t.Setenv("FCLONES_SCAN_PATHS", "/data")
	t.Setenv("FCLONES_ARGS", "--min-size 1024")
	t.Setenv("FCLONES_ACTION", "dedupe")
	t.Setenv("FCLONES_ACTION_ARGS", "--rf-over 1")
	t.Setenv("DISCORD_WEBHOOK_URL", "https://example.com/hook")
	t.Setenv("DISCORD_NOTIFY_ON_COMPLETION", "false")
	t.Setenv("DISCORD_NOTIFY_ONLY_IF_DUPLICATES", "true")

	cfg := loadConfig()

	if cfg.Schedule != "0 0 * * *" {
		t.Errorf("Schedule = %q, want %q", cfg.Schedule, "0 0 * * *")
	}
	if cfg.ScanPath != "/data" {
		t.Errorf("ScanPath = %q, want %q", cfg.ScanPath, "/data")
	}
	if cfg.Args != "--min-size 1024" {
		t.Errorf("Args = %q, want %q", cfg.Args, "--min-size 1024")
	}
	if cfg.Action != "dedupe" {
		t.Errorf("Action = %q, want %q", cfg.Action, "dedupe")
	}
	if cfg.ActionArgs != "--rf-over 1" {
		t.Errorf("ActionArgs = %q, want %q", cfg.ActionArgs, "--rf-over 1")
	}
	if cfg.WebhookURL != "https://example.com/hook" {
		t.Errorf("WebhookURL = %q, want %q", cfg.WebhookURL, "https://example.com/hook")
	}
	if cfg.NotifyOnCompletion {
		t.Error("NotifyOnCompletion should be false")
	}
	if !cfg.NotifyOnlyDuplicates {
		t.Error("NotifyOnlyDuplicates should be true")
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	cfg := loadConfig()

	if cfg.Schedule != "0 */3 * * *" {
		t.Errorf("Schedule = %q, want %q", cfg.Schedule, "0 */3 * * *")
	}
	if cfg.ScanPath != scanDir {
		t.Errorf("ScanPath = %q, want %q", cfg.ScanPath, scanDir)
	}
	if cfg.Action != "group" {
		t.Errorf("Action = %q, want %q", cfg.Action, "group")
	}
	if cfg.Args != "" {
		t.Errorf("Args = %q, want empty", cfg.Args)
	}
	if cfg.WebhookURL != "" {
		t.Errorf("WebhookURL = %q, want empty", cfg.WebhookURL)
	}
	if !cfg.NotifyOnCompletion {
		t.Error("NotifyOnCompletion should default to true")
	}
	if cfg.NotifyOnlyDuplicates {
		t.Error("NotifyOnlyDuplicates should default to false")
	}
}

func TestHealthFile(t *testing.T) {
	if os.Getenv("OS") == "Windows_NT" {
		t.Skip("skipping on Windows: /tmp does not exist")
	}

	setHealthy(true)
	defer setHealthy(false)

	if _, err := os.Stat(healthFile); err != nil {
		t.Errorf("health file should exist after setHealthy(true): %v", err)
	}

	setHealthy(false)
	if _, err := os.Stat(healthFile); err == nil {
		t.Error("health file should not exist after setHealthy(false)")
	}

	setHealthy(true)
	if _, err := os.Stat(healthFile); err != nil {
		t.Errorf("health file should exist after re-setting healthy: %v", err)
	}
}

// isDangerousArg mirrors the detection logic in rejectDangerousArgs.
// rejectDangerousArgs itself calls log.Fatalf, so we test the logic directly.
func isDangerousArg(arg string) bool {
	lower := strings.ToLower(arg)
	return lower == "--command" || strings.HasPrefix(lower, "--command=")
}

func TestRejectDangerousArgs(t *testing.T) {
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			found := slices.ContainsFunc(parseArgs(tt.input), isDangerousArg)
			if found != tt.dangerous {
				t.Errorf("parseArgs(%q): dangerous=%v, want %v", tt.input, found, tt.dangerous)
			}
		})
	}
}

func TestAllowedActions(t *testing.T) {
	for _, valid := range []string{"group", "remove", "link", "dedupe"} {
		if !allowedActions[valid] {
			t.Errorf("expected %q to be allowed", valid)
		}
	}
	for _, invalid := range []string{"", "delete", "exec", "shell", "--command"} {
		if allowedActions[invalid] {
			t.Errorf("expected %q to be rejected", invalid)
		}
	}
}

func TestSendDiscordEmptyURL(t *testing.T) {
	// Verifies the early return path — no panic, no HTTP call.
	sendDiscord("", "title", "desc", 0)
}
