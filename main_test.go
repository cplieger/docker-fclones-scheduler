package main

import (
	"os"
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"
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
	got, err := parseArgs(input)
	if err != nil {
		t.Fatalf("parseArgs(%q): unexpected error: %v", input, err)
	}
	if !slices.Equal(got, want) {
		t.Errorf("parseArgs(%q) = %v, want %v", input, got, want)
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
	t.Run("unterminated double quote", func(t *testing.T) {
		_, err := parseArgs(`--path "/unclosed`)
		if err == nil {
			t.Error("expected error for unterminated double quote")
		}
	})
	t.Run("unterminated single quote", func(t *testing.T) {
		_, err := parseArgs(`--path '/unclosed`)
		if err == nil {
			t.Error("expected error for unterminated single quote")
		}
	})
	t.Run("trailing backslash", func(t *testing.T) {
		_, err := parseArgs(`--path /test\`)
		if err == nil {
			t.Error("expected error for trailing backslash")
		}
	})
}

// Property: for inputs with no quotes or backslashes, parseArgs splits
// identically to strings.Fields (whitespace-only splitting).
func TestProperty_ParseArgsSimpleInput(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		// Generate tokens that contain no whitespace, quotes, or backslashes.
		numTokens := rapid.IntRange(0, 10).Draw(rt, "numTokens")
		tokens := make([]string, numTokens)
		for i := range numTokens {
			tokens[i] = rapid.StringMatching(`[a-zA-Z0-9\-_./=]{1,20}`).Draw(rt, "token")
		}
		input := strings.Join(tokens, " ")

		got, err := parseArgs(input)
		if err != nil {
			rt.Fatalf("unexpected error for simple input %q: %v", input, err)
		}

		want := strings.Fields(input)
		if !slices.Equal(got, want) {
			rt.Fatalf("parseArgs(%q) = %v, want %v (strings.Fields)", input, got, want)
		}
	})
}

// Property: double-quoted content is preserved exactly, and the output
// never contains the quote characters themselves.
func TestProperty_ParseArgsQuotedContent(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		// Generate content that doesn't contain quotes or backslashes.
		content := rapid.StringMatching(`[a-zA-Z0-9 _\-./]{1,30}`).Draw(rt, "content")
		input := `"` + content + `"`

		got, err := parseArgs(input)
		if err != nil {
			rt.Fatalf("unexpected error for quoted input %q: %v", input, err)
		}

		if len(got) != 1 {
			rt.Fatalf("expected 1 arg, got %d: %v", len(got), got)
		}
		if got[0] != content {
			rt.Fatalf("quoted content not preserved: got %q, want %q", got[0], content)
		}
		if strings.ContainsAny(got[0], `"'`) {
			rt.Fatalf("output contains quote characters: %q", got[0])
		}
	})
}

func TestLoadConfig(t *testing.T) {
	t.Setenv("FCLONES_SCHEDULE", "0 0 * * *")
	t.Setenv("FCLONES_SCAN_PATHS", "/data")
	t.Setenv("FCLONES_ARGS", "--min-size 1024")
	t.Setenv("FCLONES_ACTION", "dedupe")
	t.Setenv("FCLONES_ACTION_ARGS", "--rf-over 1")

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
// rejectDangerousArgs calls os.Exit, so we test the logic directly.
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
			args, err := parseArgs(tt.input)
			if err != nil {
				t.Fatalf("parseArgs: %v", err)
			}
			found := slices.ContainsFunc(args, isDangerousArg)
			if found != tt.dangerous {
				t.Errorf("parseArgs(%q): dangerous=%v, want %v", tt.input, found, tt.dangerous)
			}
		})
	}
}

func TestBuildScanArgs(t *testing.T) {
	t.Run("basic config", func(t *testing.T) {
		cfg := &config{ScanPath: "/data", Args: "--min-size 1024"}
		args, err := buildScanArgs(cfg)
		if err != nil {
			t.Fatalf("buildScanArgs: %v", err)
		}
		if args[0] != "group" {
			t.Errorf("args[0] = %q, want group", args[0])
		}
		if args[len(args)-1] != "--cache" {
			t.Errorf("last arg = %q, want --cache", args[len(args)-1])
		}
	})

	t.Run("invalid scan path quotes", func(t *testing.T) {
		cfg := &config{ScanPath: `"/unclosed`, Args: ""}
		_, err := buildScanArgs(cfg)
		if err == nil {
			t.Error("expected error for unterminated quote in scan path")
		}
	})

	t.Run("invalid args quotes", func(t *testing.T) {
		cfg := &config{ScanPath: "/data", Args: `--flag "unclosed`}
		_, err := buildScanArgs(cfg)
		if err == nil {
			t.Error("expected error for unterminated quote in args")
		}
	})

	t.Run("empty args", func(t *testing.T) {
		cfg := &config{ScanPath: "/data", Args: ""}
		args, err := buildScanArgs(cfg)
		if err != nil {
			t.Fatalf("buildScanArgs: %v", err)
		}
		// group + /data + --cache = 3
		if len(args) != 3 {
			t.Errorf("len = %d, want 3", len(args))
		}
	})
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
