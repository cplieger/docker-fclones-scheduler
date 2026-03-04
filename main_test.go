package main

import (
	"os"
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
			name:   "standard output",
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
			name:   "real fclones output with commas",
			input:  "# Redundant: 1,234 files (5.6 GB) in 789 groups\n# Total: 2,000 789 groups",
			groups: "789",
			size:   "5.6 GB",
		},
		{
			name:   "empty input",
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
			name:  "no duplicates",
			input: "# some comment\n",
			want:  "No duplicates found.",
		},
		{
			name:  "single group",
			input: "# comment\n\n/path/to/file1\n/path/to/file2\n",
			want:  "/path/to/file1\n↳ /path/to/file2\n",
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
			name:  "with processed line",
			input: "some output\nProcessed 5 files, reclaimed 1.2 GB\nmore output",
			want:  "Processed 5 files, reclaimed 1.2 GB",
		},
		{
			name:  "no processed line",
			input: "some output\nlast line",
			want:  "last line",
		},
		{
			name:  "empty",
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

func TestParseArgs(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "simple args",
			input: "--min-size 1024 --threads 4",
			want:  []string{"--min-size", "1024", "--threads", "4"},
		},
		{
			name:  "quoted args",
			input: `--path "/some dir/with spaces" --name 'test file'`,
			want:  []string{"--path", "/some dir/with spaces", "--name", "test file"},
		},
		{
			name:  "empty",
			input: "",
			want:  nil,
		},
		{
			name:  "single arg",
			input: "--rf-over 1",
			want:  []string{"--rf-over", "1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseArgs(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("len = %d, want %d; got %v", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
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
	// Don't set any env vars — test that defaults are applied
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

func TestHealthFileCreatedOnHealthy(t *testing.T) {
	// Skip on Windows — /tmp doesn't exist; this runs in Linux containers
	if os.Getenv("OS") == "Windows_NT" {
		t.Skip("skipping on Windows: /tmp does not exist")
	}

	setHealthy(true)
	if _, err := os.Stat(healthFile); err != nil {
		t.Errorf("health file should exist after setHealthy(true): %v", err)
	}

	setHealthy(false)
	if _, err := os.Stat(healthFile); err == nil {
		t.Error("health file should not exist after setHealthy(false)")
	}

	// Restore healthy state
	setHealthy(true)
	defer setHealthy(false)
	if _, err := os.Stat(healthFile); err != nil {
		t.Errorf("health file should exist after re-setting healthy: %v", err)
	}
}
