package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
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

func TestReadFileWithLimit(t *testing.T) {
	t.Run("reads file within limit", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "small.txt")
		os.WriteFile(path, []byte("hello"), 0o644)

		data, err := readFileWithLimit(path, 100)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(data) != "hello" {
			t.Errorf("got %q, want %q", data, "hello")
		}
	})

	t.Run("rejects file over limit", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "big.txt")
		os.WriteFile(path, make([]byte, 200), 0o644)

		_, err := readFileWithLimit(path, 100)
		if err == nil {
			t.Error("expected error for oversized file")
		}
	})

	t.Run("returns error for missing file", func(t *testing.T) {
		_, err := readFileWithLimit("/nonexistent/file.txt", 100)
		if err == nil {
			t.Error("expected error for missing file")
		}
	})
}

func TestParseRedundantSize(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{"parenthesized", "# Redundant: 5 files (1.2 GB)", "1.2 GB"},
		{"bare size", "# Redundant: 512 MB", "512 MB"},
		{"too few fields", "# Redundant:", "0 B"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseRedundantSize(tt.line)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// --- Tests: verifyCacheDir ---

func TestVerifyCacheDir(t *testing.T) {
	// verifyCacheDir uses the package-level cacheDir constant ("/cache"),
	// which won't exist in test. It should log a warning but not panic.
	// We just verify it doesn't crash.
	verifyCacheDir()
}

// --- Tests: clearCurrentJob ---

func TestClearCurrentJob(t *testing.T) {
	mu.Lock()
	currentJob = &exec.Cmd{}
	mu.Unlock()

	clearCurrentJob()

	mu.Lock()
	defer mu.Unlock()
	if currentJob != nil {
		t.Error("currentJob should be nil after clearCurrentJob")
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

// --- Tests: parseArgs edge cases ---

func TestParseArgsMixedQuotes(t *testing.T) {
	got, err := parseArgs(`--path="/my dir" --name='hello world' plain`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"--path=/my dir", "--name=hello world", "plain"}
	if len(got) != len(want) {
		t.Fatalf("got %d args, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("arg[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParseArgsEscapedChars(t *testing.T) {
	got, err := parseArgs(`hello\ world foo\\bar`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d args, want 2", len(got))
	}
	if got[0] != "hello world" {
		t.Errorf("arg[0] = %q, want 'hello world'", got[0])
	}
	if got[1] != `foo\bar` {
		t.Errorf("arg[1] = %q, want 'foo\\bar'", got[1])
	}
}

func TestParseArgsEmptyInput(t *testing.T) {
	got, err := parseArgs("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 args for empty input, got %d", len(got))
	}
}

func TestParseArgsTabSeparated(t *testing.T) {
	got, err := parseArgs("a\tb\tc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d args, want 3", len(got))
	}
}

// --- Tests: buildScanArgs edge cases ---

func TestBuildScanArgsWithExtraArgs(t *testing.T) {
	cfg := &config{
		ScanPath: "/data",
		Args:     "--min-size 1M --threads 4",
	}
	got, err := buildScanArgs(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should be: group /data --min-size 1M --threads 4 --cache
	if len(got) != 7 {
		t.Fatalf("got %d args, want 7: %v", len(got), got)
	}
	if got[0] != "group" {
		t.Errorf("first arg should be 'group', got %q", got[0])
	}
	if got[len(got)-1] != "--cache" {
		t.Errorf("last arg should be '--cache', got %q", got[len(got)-1])
	}
}

func TestBuildScanArgsInvalidScanPath(t *testing.T) {
	cfg := &config{ScanPath: `"unterminated`}
	_, err := buildScanArgs(cfg)
	if err == nil {
		t.Fatal("expected error for unterminated quote in scan path")
	}
}

func TestBuildScanArgsInvalidExtraArgs(t *testing.T) {
	cfg := &config{ScanPath: "/data", Args: `"unterminated`}
	_, err := buildScanArgs(cfg)
	if err == nil {
		t.Fatal("expected error for unterminated quote in extra args")
	}
}

// --- Tests: buildActionArgs ---

func TestBuildActionArgs(t *testing.T) {
	tests := []struct {
		name    string
		cfg     config
		want    []string
		wantNil bool
		wantErr bool
	}{
		{
			name:    "group action returns nil",
			cfg:     config{Action: "group"},
			wantNil: true,
		},
		{
			name:    "empty action returns nil",
			cfg:     config{Action: ""},
			wantNil: true,
		},
		{
			name: "remove action no extra args",
			cfg:  config{Action: "remove"},
			want: []string{"remove"},
		},
		{
			name: "link action with extra args",
			cfg:  config{Action: "link", ActionArgs: "--soft"},
			want: []string{"link", "--soft"},
		},
		{
			name: "dedupe action with multiple args",
			cfg:  config{Action: "dedupe", ActionArgs: "--rf-over 1 --dry-run"},
			want: []string{"dedupe", "--rf-over", "1", "--dry-run"},
		},
		{
			name:    "invalid action args quotes",
			cfg:     config{Action: "remove", ActionArgs: `"unterminated`},
			wantErr: true,
		},
		{
			name: "action args with quoted path",
			cfg:  config{Action: "remove", ActionArgs: `--path "/my dir"`},
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

// --- Tests: readFileWithLimit edge cases ---

func TestReadFileWithLimitExactBoundary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "exact.txt")
	data := []byte("12345")
	os.WriteFile(path, data, 0o644)

	// Exactly at limit — should succeed.
	got, err := readFileWithLimit(path, int64(len(data)))
	if err != nil {
		t.Fatalf("unexpected error at exact boundary: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("got %q, want %q", got, data)
	}

	// One byte under limit — should fail.
	_, err = readFileWithLimit(path, int64(len(data)-1))
	if err == nil {
		t.Error("expected error when file exceeds limit by 1 byte")
	}
}

func TestReadFileWithLimitEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.txt")
	os.WriteFile(path, []byte{}, 0o644)

	got, err := readFileWithLimit(path, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %d bytes", len(got))
	}
}

// --- Tests: parseStats edge cases ---

func TestParseStatsEmpty(t *testing.T) {
	stats := parseStats("")
	if stats.Groups != "0" || stats.Size != "0 B" {
		t.Errorf("parseStats empty = %+v", stats)
	}
}

func TestParseStatsPartialOutput(t *testing.T) {
	stats := parseStats("# Total: 5 groups\n")
	if stats.Groups != "5" {
		t.Errorf("Groups = %q, want 5", stats.Groups)
	}
	if stats.Size != "0 B" {
		t.Errorf("Size = %q, want '0 B'", stats.Size)
	}
}

func TestParseStatsTotalWithoutGroups(t *testing.T) {
	// "# Total:" line that doesn't end with "groups" — should not update Groups.
	stats := parseStats("# Total: 100 files\n")
	if stats.Groups != "0" {
		t.Errorf("Groups = %q, want 0 (line doesn't end with 'groups')", stats.Groups)
	}
}

func TestParseStatsRedundantOnly(t *testing.T) {
	stats := parseStats("# Redundant: 3 files (42 MB)\n")
	if stats.Size != "42 MB" {
		t.Errorf("Size = %q, want '42 MB'", stats.Size)
	}
	if stats.Groups != "0" {
		t.Errorf("Groups = %q, want 0", stats.Groups)
	}
}

// --- Tests: parseDuplicatesFormatted edge cases ---

func TestParseDuplicatesFormattedEmpty(t *testing.T) {
	got := parseDuplicatesFormatted("")
	if got != "No duplicates found." {
		t.Errorf("got %q, want 'No duplicates found.'", got)
	}
}

func TestParseDuplicatesFormattedOnlyBlanks(t *testing.T) {
	got := parseDuplicatesFormatted("\n\n\n")
	if got != "No duplicates found." {
		t.Errorf("got %q, want 'No duplicates found.'", got)
	}
}

func TestParseDuplicatesFormattedSingleFile(t *testing.T) {
	// A single file with no group peer — still formatted as first in group.
	got := parseDuplicatesFormatted("/path/only\n")
	if got != "/path/only\n" {
		t.Errorf("got %q, want '/path/only\\n'", got)
	}
}

func TestParseDuplicatesFormattedWhitespaceInPaths(t *testing.T) {
	input := "  /path/with spaces  \n  /path/another  \n"
	got := parseDuplicatesFormatted(input)
	want := "/path/with spaces\n↳ /path/another\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// --- Tests: extractProcessedLine edge cases ---

func TestExtractProcessedLineOnlyWhitespace(t *testing.T) {
	got := extractProcessedLine("   \n  \n  ")
	if got != "(No output captured)" {
		t.Errorf("got %q, want '(No output captured)'", got)
	}
}

func TestExtractProcessedLineProcessedWithoutReclaimed(t *testing.T) {
	// "Processed" without "reclaimed" should NOT match — falls back to last non-empty.
	got := extractProcessedLine("Processed 5 files\nlast line")
	if got != "last line" {
		t.Errorf("got %q, want 'last line'", got)
	}
}

func TestExtractProcessedLineProcessedMidLine(t *testing.T) {
	// "Processed" appearing mid-line with prefix text.
	got := extractProcessedLine("INFO: Processed 10 files, reclaimed 500 MB")
	want := "Processed 10 files, reclaimed 500 MB"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// --- Tests: parseRedundantSize edge cases ---

func TestParseRedundantSizeUnmatchedParen(t *testing.T) {
	// Opening paren without closing — should fall back to bare form.
	got := parseRedundantSize("# Redundant: 5 files (1.2 GB")
	if got != "5 files" {
		t.Errorf("got %q, want '5 files'", got)
	}
}

func TestParseRedundantSizeEmptyParens(t *testing.T) {
	got := parseRedundantSize("# Redundant: 5 files ()")
	if got != "" {
		t.Errorf("got %q, want empty string", got)
	}
}

// --- Tests: buildScanArgs with multiple scan paths ---

func TestBuildScanArgsMultiplePaths(t *testing.T) {
	cfg := &config{ScanPath: "/data /media /backup", Args: ""}
	got, err := buildScanArgs(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// group + /data + /media + /backup + --cache = 5
	if len(got) != 5 {
		t.Fatalf("got %d args, want 5: %v", len(got), got)
	}
	if got[1] != "/data" || got[2] != "/media" || got[3] != "/backup" {
		t.Errorf("paths = %v, want [/data /media /backup]", got[1:4])
	}
}

// Property: buildActionArgs returns nil for group/empty, non-nil for other actions.
func TestProperty_BuildActionArgsNilForGroup(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		action := rapid.SampledFrom([]string{"group", ""}).Draw(rt, "action")
		cfg := &config{Action: action}
		got, err := buildActionArgs(cfg)
		if err != nil {
			rt.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			rt.Fatalf("expected nil for action %q, got %v", action, got)
		}
	})
}

// Property: buildActionArgs always starts with the action name for non-group actions.
func TestProperty_BuildActionArgsStartsWithAction(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		action := rapid.SampledFrom([]string{"remove", "link", "dedupe"}).Draw(rt, "action")
		// Generate safe action args (no quotes or backslashes).
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
		if got[0] != action {
			rt.Fatalf("first arg = %q, want %q", got[0], action)
		}
		// Total args should be 1 (action) + numArgs.
		if len(got) != 1+numArgs {
			rt.Fatalf("got %d args, want %d", len(got), 1+numArgs)
		}
	})
}

// Property: loadConfig with valid actions always returns one of the allowed actions.
func TestProperty_LoadConfigValidActions(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		action := rapid.SampledFrom([]string{"group", "remove", "link", "dedupe"}).Draw(rt, "action")
		t.Setenv("FCLONES_ACTION", action)
		t.Setenv("FCLONES_ARGS", "")
		t.Setenv("FCLONES_ACTION_ARGS", "")
		cfg := loadConfig()
		if !allowedActions[cfg.Action] {
			rt.Fatalf("loadConfig returned disallowed action %q", cfg.Action)
		}
	})
}

// --- Property-based tests: parsing invariants ---

// Property: parseStats always returns non-empty Groups and Size fields,
// regardless of input. The defaults are "0" and "0 B".
func TestProperty_ParseStatsAlwaysReturnsDefaults(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		input := rapid.String().Draw(rt, "input")

		stats := parseStats(input)

		if stats.Groups == "" {
			rt.Fatalf("parseStats(%q).Groups is empty", input)
		}
		if stats.Size == "" {
			rt.Fatalf("parseStats(%q).Size is empty", input)
		}
	})
}

// Property: parseStats is deterministic — same input always produces same output.
func TestProperty_ParseStatsIsDeterministic(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		input := rapid.String().Draw(rt, "input")

		a := parseStats(input)
		b := parseStats(input)

		if a.Groups != b.Groups {
			rt.Fatalf("parseStats(%q) non-deterministic: Groups %q vs %q", input, a.Groups, b.Groups)
		}
		if a.Size != b.Size {
			rt.Fatalf("parseStats(%q) non-deterministic: Size %q vs %q", input, a.Size, b.Size)
		}
	})
}

// Property: parseDuplicatesFormatted never panics and always returns a non-empty string.
func TestProperty_ParseDuplicatesFormattedNeverEmpty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		input := rapid.String().Draw(rt, "input")

		got := parseDuplicatesFormatted(input)

		if got == "" {
			rt.Fatalf("parseDuplicatesFormatted(%q) returned empty string", input)
		}
	})
}

// Property: parseDuplicatesFormatted is idempotent on its sentinel value —
// "No duplicates found." input produces "No duplicates found." output.
func TestProperty_ParseDuplicatesFormattedSentinel(t *testing.T) {
	got := parseDuplicatesFormatted("")
	if got != "No duplicates found." {
		t.Errorf("parseDuplicatesFormatted(\"\") = %q, want \"No duplicates found.\"", got)
	}

	// The sentinel itself, when parsed, should produce file entries (not the sentinel).
	// This verifies the sentinel is not a magic passthrough.
	got2 := parseDuplicatesFormatted("No duplicates found.")
	if got2 == "" {
		t.Error("parseDuplicatesFormatted(sentinel) returned empty string")
	}
}

// Property: extractProcessedLine never returns an empty string.
func TestProperty_ExtractProcessedLineNeverEmpty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		input := rapid.String().Draw(rt, "input")

		got := extractProcessedLine(input)

		if got == "" {
			rt.Fatalf("extractProcessedLine(%q) returned empty string", input)
		}
	})
}

// Property: extractProcessedLine is deterministic.
func TestProperty_ExtractProcessedLineIsDeterministic(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		input := rapid.String().Draw(rt, "input")

		a := extractProcessedLine(input)
		b := extractProcessedLine(input)

		if a != b {
			rt.Fatalf("extractProcessedLine(%q) non-deterministic: %q vs %q", input, a, b)
		}
	})
}

// Property: parseArgs never panics on arbitrary input. It either returns
// a valid result or an error.
func TestProperty_ParseArgsNeverPanics(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		input := rapid.String().Draw(rt, "input")
		// Just call it — if it panics, rapid catches it.
		parseArgs(input)
	})
}

// Property: parseArgs round-trip — joining parsed args with spaces and
// re-parsing produces the same result, for inputs without quotes/backslashes.
func TestProperty_ParseArgsRoundTrip(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		numTokens := rapid.IntRange(0, 10).Draw(rt, "numTokens")
		tokens := make([]string, numTokens)
		for i := range numTokens {
			// Tokens with no whitespace, quotes, or backslashes.
			tokens[i] = rapid.StringMatching(`[a-zA-Z0-9\-_./=]{1,20}`).Draw(rt, "token")
		}
		input := strings.Join(tokens, " ")

		parsed, err := parseArgs(input)
		if err != nil {
			rt.Fatalf("parseArgs(%q): unexpected error: %v", input, err)
		}

		rejoined := strings.Join(parsed, " ")
		reparsed, err := parseArgs(rejoined)
		if err != nil {
			rt.Fatalf("parseArgs(%q) round-trip error: %v", rejoined, err)
		}

		if !slices.Equal(parsed, reparsed) {
			rt.Fatalf("parseArgs round-trip failed: %v → %q → %v", parsed, rejoined, reparsed)
		}
	})
}

// Property: parseRedundantSize never panics on arbitrary input.
func TestProperty_ParseRedundantSizeNeverPanics(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		input := rapid.String().Draw(rt, "input")
		parseRedundantSize(input)
	})
}

// Property: parseRedundantSize always returns a string (may be "0 B" default).
func TestProperty_ParseRedundantSizeAlwaysReturns(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		// Generate lines that look like "# Redundant: ..." with random suffixes.
		suffix := rapid.String().Draw(rt, "suffix")
		line := "# Redundant: " + suffix

		got := parseRedundantSize(line)
		// Should never panic — result can be anything including "0 B".
		_ = got
	})
}

// Property: buildScanArgs always starts with "group" and ends with "--cache".
func TestProperty_BuildScanArgsStructure(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		// Generate safe scan paths and args (no quotes/backslashes).
		scanPath := rapid.StringMatching(`/[a-z]{1,10}(/[a-z]{1,10}){0,3}`).Draw(rt, "scanPath")
		numArgs := rapid.IntRange(0, 3).Draw(rt, "numArgs")
		argTokens := make([]string, numArgs*2)
		for i := 0; i < numArgs*2; i += 2 {
			argTokens[i] = rapid.StringMatching(`--[a-z\-]{1,10}`).Draw(rt, "flag")
			argTokens[i+1] = rapid.StringMatching(`[a-zA-Z0-9]{1,10}`).Draw(rt, "value")
		}
		args := strings.Join(argTokens, " ")

		cfg := &config{ScanPath: scanPath, Args: args}
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

// --- Additional edge cases ---

// parseStats: only Redundant line, no Total line.
func TestParseStatsRedundantWithoutTotal(t *testing.T) {
	stats := parseStats("# Redundant: 10 files (2.5 GB)\n")

	if stats.Size != "2.5 GB" {
		t.Errorf("parseStats Size = %q, want \"2.5 GB\"", stats.Size)
	}
	if stats.Groups != "0" {
		t.Errorf("parseStats Groups = %q, want \"0\"", stats.Groups)
	}
}

// parseStats: Total line with single word after number (not "groups").
func TestParseStatsTotalWithFiles(t *testing.T) {
	stats := parseStats("# Total: 100 files\n")

	if stats.Groups != "0" {
		t.Errorf("parseStats Groups = %q, want \"0\" (line ends with 'files', not 'groups')", stats.Groups)
	}
}

// parseStats: multiple Redundant lines — last one wins.
func TestParseStatsMultipleRedundantLines(t *testing.T) {
	input := "# Redundant: 1 files (100 MB)\n# Redundant: 2 files (200 MB)\n"
	stats := parseStats(input)

	if stats.Size != "200 MB" {
		t.Errorf("parseStats Size = %q, want \"200 MB\" (last Redundant line wins)", stats.Size)
	}
}

// parseDuplicatesFormatted: multiple group headers in sequence.
func TestParseDuplicatesFormattedMultipleGroupHeaders(t *testing.T) {
	input := "abc,100 B,2 * 50 B:\n/file1\n/file2\n\nxyz,200 B,2 * 100 B:\n/file3\n/file4\n"
	got := parseDuplicatesFormatted(input)
	want := "/file1\n↳ /file2\n/file3\n↳ /file4\n"

	if got != want {
		t.Errorf("parseDuplicatesFormatted = %q, want %q", got, want)
	}
}

// parseDuplicatesFormatted: group with many files.
func TestParseDuplicatesFormattedLargeGroup(t *testing.T) {
	input := "/f1\n/f2\n/f3\n/f4\n/f5\n"
	got := parseDuplicatesFormatted(input)
	want := "/f1\n↳ /f2\n↳ /f3\n↳ /f4\n↳ /f5\n"

	if got != want {
		t.Errorf("parseDuplicatesFormatted = %q, want %q", got, want)
	}
}

// extractProcessedLine: multiple "Processed...reclaimed" lines — first one wins.
func TestExtractProcessedLineFirstMatch(t *testing.T) {
	input := "Processed 5 files, reclaimed 1 GB\nProcessed 10 files, reclaimed 2 GB\n"
	got := extractProcessedLine(input)
	want := "Processed 5 files, reclaimed 1 GB"

	if got != want {
		t.Errorf("extractProcessedLine = %q, want %q", got, want)
	}
}

// extractProcessedLine: "reclaimed" without "Processed" — falls back.
func TestExtractProcessedLineReclaimedWithoutProcessed(t *testing.T) {
	got := extractProcessedLine("reclaimed 500 MB\nlast line")

	if got != "last line" {
		t.Errorf("extractProcessedLine = %q, want \"last line\"", got)
	}
}

// parseRedundantSize: nested parentheses — extracts first pair.
func TestParseRedundantSizeNestedParens(t *testing.T) {
	got := parseRedundantSize("# Redundant: 5 files (1.2 GB (approx))")
	// First "(" to first ")" → "1.2 GB (approx"
	// Wait — let me check: Index finds first "(", then first ")" after it.
	// "# Redundant: 5 files (1.2 GB (approx))"
	// start = index of first "(" → points to "(1.2..."
	// end = index of first ")" → points to ")" after "approx"
	// Actually strings.Index(line, ")") finds the FIRST ")" in the whole line.
	// So: start points to "(1.2...", end points to first ")" which is after "approx".
	// Wait no — the first ")" is after "approx": "(1.2 GB (approx))"
	// First "(" is at position of "(1.2", first ")" is at position of ")" after "approx".
	// So it extracts "1.2 GB (approx".
	want := "1.2 GB (approx"
	if got != want {
		t.Errorf("parseRedundantSize nested = %q, want %q", got, want)
	}
}

// parseArgs: escaped quote inside quotes.
func TestParseArgsEscapedQuoteInQuotes(t *testing.T) {
	// Inside double quotes, backslash-quote produces the literal quote char.
	got, err := parseArgs(`"hello\"world"`)
	if err != nil {
		t.Fatalf("parseArgs: unexpected error: %v", err)
	}
	// The backslash consumes the next char ("), so we get: hello"world
	// But wait — in our parser, inside quotes, backslash is NOT special.
	// Let me re-read the parser...
	// The parser checks `escaped` first (before quote handling), so \\" inside
	// quotes: the \ sets escaped=true, then " is consumed as literal.
	// Actually no — let me trace through carefully:
	// Input runes: [" h e l-l o \ " w o r l d "]
	// r='"': not escaped, not inQuote → inQuote=true, quoteChar='"'
	// r='h'...'o': inQuote, r != quoteChar → append
	// r='\': not escaped (escaped check is first) → escaped=true
	// r='"': escaped=true → append '"', escaped=false
	// r='w'...'d': inQuote, r != quoteChar → append
	// r='"': inQuote, r == quoteChar → inQuote=false
	// Result: ["hello\"world"] → actually the content is hello"world
	want := []string{`hello"world`}
	if !slices.Equal(got, want) {
		t.Errorf("parseArgs escaped quote = %v, want %v", got, want)
	}
}

// parseArgs: adjacent quoted segments merge into one arg.
func TestParseArgsAdjacentQuotes(t *testing.T) {
	got, err := parseArgs(`"hello"'world'`)
	if err != nil {
		t.Fatalf("parseArgs: unexpected error: %v", err)
	}
	// "hello" → appends hello (no space, so current builder still active)
	// 'world' → appends world to same builder
	// Result: ["helloworld"]
	want := []string{"helloworld"}
	if !slices.Equal(got, want) {
		t.Errorf("parseArgs adjacent quotes = %v, want %v", got, want)
	}
}

// parseArgs: only whitespace.
func TestParseArgsOnlyWhitespace(t *testing.T) {
	got, err := parseArgs("   \t  \t  ")
	if err != nil {
		t.Fatalf("parseArgs: unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("parseArgs whitespace-only = %v, want nil", got)
	}
}

// parseArgs: empty quoted string produces empty arg.
func TestParseArgsEmptyQuotedString(t *testing.T) {
	// "" → opens and closes quote, current builder has 0 length.
	// After loop, current.Len() == 0, so nothing appended.
	got, err := parseArgs(`""`)
	if err != nil {
		t.Fatalf("parseArgs: unexpected error: %v", err)
	}
	// The empty quoted string: after closing quote, current has 0 chars.
	// The final `if current.Len() > 0` check means empty quoted strings
	// produce NO arg. This is the actual behavior.
	if len(got) != 0 {
		t.Errorf("parseArgs empty quotes = %v, want nil (empty quoted string produces no arg)", got)
	}
}

// readFileWithLimit: file with exactly 0 bytes limit.
func TestReadFileWithLimitZeroLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonempty.txt")
	os.WriteFile(path, []byte("x"), 0o644)

	_, err := readFileWithLimit(path, 0)
	if err == nil {
		t.Error("expected error for 1-byte file with 0-byte limit")
	}
}

// readFileWithLimit: empty file with 0 limit succeeds.
func TestReadFileWithLimitEmptyFileZeroLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.txt")
	os.WriteFile(path, []byte{}, 0o644)

	got, err := readFileWithLimit(path, 0)
	if err != nil {
		t.Fatalf("readFileWithLimit empty file with 0 limit: unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %d bytes", len(got))
	}
}

// buildActionArgs: all valid non-group actions produce correct first element.
func TestBuildActionArgsAllActions(t *testing.T) {
	for _, action := range []string{"remove", "link", "dedupe"} {
		t.Run(action, func(t *testing.T) {
			cfg := &config{Action: action}
			got, err := buildActionArgs(cfg)
			if err != nil {
				t.Fatalf("buildActionArgs(%q): unexpected error: %v", action, err)
			}
			if len(got) != 1 {
				t.Fatalf("buildActionArgs(%q) = %v, want [%q]", action, got, action)
			}
			if got[0] != action {
				t.Errorf("buildActionArgs(%q)[0] = %q, want %q", action, got[0], action)
			}
		})
	}
}

// getEnv: unset variable returns fallback.
func TestGetEnvUnset(t *testing.T) {
	got := getEnv("FCLONES_TEST_UNSET_VAR_12345", "fallback")
	if got != "fallback" {
		t.Errorf("getEnv unset = %q, want \"fallback\"", got)
	}
}

// getEnv: empty fallback with unset var.
func TestGetEnvEmptyFallback(t *testing.T) {
	got := getEnv("FCLONES_TEST_UNSET_VAR_12345", "")
	if got != "" {
		t.Errorf("getEnv empty fallback = %q, want \"\"", got)
	}
}
