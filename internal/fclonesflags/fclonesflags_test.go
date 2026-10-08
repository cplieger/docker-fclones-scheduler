package fclonesflags

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	helpDir   = "testdata/help"
	regenHint = "UPDATE_GOLDEN=1 FCLONES_BIN=<path to fclones> go test ./internal/fclonesflags -run TestBinaryMatchesSnapshot"
)

// fixtureName maps a help invocation to its testdata file stem.
func fixtureName(args []string) string {
	if len(args) == 1 {
		return "fclones"
	}
	return args[0]
}

// fixtureHelp serves the committed help texts and refuses any invocation
// other than "--help" or "<command> --help".
func fixtureHelp(t *testing.T) helpFunc {
	t.Helper()
	return func(args ...string) (string, error) {
		ok := (len(args) == 1 && args[0] == "--help") || (len(args) == 2 && args[1] == "--help")
		if !ok {
			t.Errorf("help called with %q, want [--help] or [<command> --help]", args)
			return "", errors.New("unexpected invocation")
		}
		b, err := os.ReadFile(filepath.Join(helpDir, fixtureName(args)+".txt"))
		return string(b), err
	}
}

func TestInventory_realHelpEqualsSnapshot(t *testing.T) {
	t.Parallel()
	got, err := inventory(fixtureHelp(t))
	if err != nil {
		t.Fatalf("inventory(committed help) error: %v", err)
	}
	want := Snapshot()
	if added, removed := diff(want, got); len(added)+len(removed) > 0 {
		t.Errorf("inventory(committed help) differs from flags.txt: added %q, removed %q; regenerate both with %s",
			added, removed, regenHint)
	}
	for _, e := range []string{"fclones --progress", "group --transform", "group -.", "link --priority", "dedupe --dry-run"} {
		if _, found := slices.BinarySearch(want, e); !found {
			t.Errorf("Snapshot() lacks %q", e)
		}
	}
}

func TestOptions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, help string
		want       []string
	}{
		{"short and long", "  -o, --output <PATH>\n", []string{"--output", "-o"}},
		{"long only beside shorts", "      --stdin\n", []string{"--stdin"}},
		{"long only at column 2", "  --stdin\n", []string{"--stdin"}},
		{"value after equals", "      --progress=<VAL>  Override progress\n", []string{"--progress"}},
		{"optional value", "      --color[=<WHEN>]\n", []string{"--color"}},
		{"punctuation short", "  -., --hidden\n", []string{"--hidden", "-."}},
		{"digit short", "  -1, --one-fs\n", []string{"--one-fs", "-1"}},
		{"short only", "  -V\n", []string{"-V"}},
		{"crlf line", "  -h, --help\r\n", []string{"--help", "-h"}},
		{"deduplicated", "  -h, --help\n  -h, --help\n", []string{"--help", "-h"}},
		{"description quoting a flag", "          `--depth`.\n          --transform here\n", nil},
		{"wrapped short-help description", "                        --in-place\n", nil},
		{"positional argument", "  [PATHS]...\n", nil},
		{"usage line", "Usage: fclones group [OPTIONS] [PATHS]...\n", nil},
		{"list item", "          - top:   Give higher priority\n", nil},
		{"flag prefix is not a name", "  --=x\n", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := options(tc.help); !slices.Equal(got, tc.want) {
				t.Errorf("options(%q) = %q, want %q", tc.help, got, tc.want)
			}
		})
	}
}

func TestCommands(t *testing.T) {
	t.Parallel()
	help := "Usage: fclones [OPTIONS] <COMMAND>\n\nCommands:\n" +
		"  group     Produce a list\n  link      Replace\n  help      Print this message\n\n" +
		"Options:\n  -h, --help  Print help\n\nExamples:\n  fclones group .\n"
	want := []string{"group", "link"}
	if got := commands(help); !slices.Equal(got, want) {
		t.Errorf("commands(%q) = %q, want %q", help, got, want)
	}
	if got := commands("Options:\n  -h, --help\n"); got != nil {
		t.Errorf("commands(no Commands section) = %q, want nil", got)
	}
}

func TestInventory_failsClosed(t *testing.T) {
	t.Parallel()
	const root = "Commands:\n  group  Produce\n\nOptions:\n  -h, --help\n"
	tests := []struct {
		help    helpFunc
		name    string
		wantErr string
	}{
		{
			name:    "no subcommand listed",
			help:    func(...string) (string, error) { return "Options:\n  -h, --help\n", nil },
			wantErr: "lists no subcommand",
		},
		{
			name: "subcommand help lists no option",
			help: func(args ...string) (string, error) {
				if len(args) == 1 {
					return root, nil
				}
				return "Usage: fclones group\n", nil
			},
			wantErr: `help of "group" lists no option`,
		},
		{
			name:    "top-level help fails",
			help:    func(...string) (string, error) { return "", errors.New("exec: not found") },
			wantErr: "fclones --help: exec: not found",
		},
		{
			name: "subcommand help fails",
			help: func(args ...string) (string, error) {
				if len(args) == 1 {
					return root, nil
				}
				return "", errors.New("exit status 2")
			},
			wantErr: "fclones group --help: exit status 2",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := inventory(tc.help)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("inventory() error = %v, want it to contain %q", err, tc.wantErr)
			}
			if got != nil {
				t.Errorf("inventory() entries = %q, want nil on error", got)
			}
		})
	}
}

func TestDiff(t *testing.T) {
	t.Parallel()
	want := []string{"group --a", "group --b", "link --c"}
	got := []string{"group --b", "link --c", "link --d"}
	added, removed := diff(want, got)
	if !slices.Equal(added, []string{"link --d"}) {
		t.Errorf("diff() added = %q, want [link --d]", added)
	}
	if !slices.Equal(removed, []string{"group --a"}) {
		t.Errorf("diff() removed = %q, want [group --a]", removed)
	}
	if a, r := diff(want, want); a != nil || r != nil {
		t.Errorf("diff(same, same) = %q, %q, want nil, nil", a, r)
	}
}

func TestParseList_formatListRoundTrip(t *testing.T) {
	t.Parallel()
	text := "# header\n\nlink  --c\ngroup --a\n  group --a  \n#group --z\n"
	want := []string{"group --a", "link --c"}
	got := parseList(text)
	if !slices.Equal(got, want) {
		t.Fatalf("parseList(%q) = %q, want %q", text, got, want)
	}
	formatted := formatList("line one\nline two", got)
	if !strings.HasPrefix(formatted, "# line one\n# line two\n") {
		t.Errorf("formatList() header = %q, want each header line commented", formatted)
	}
	if back := parseList(formatted); !slices.Equal(back, want) {
		t.Errorf("parseList(formatList()) = %q, want %q", back, want)
	}
}

// TestBinaryMatchesSnapshot is the image build's gate: it compares the
// options of the fclones binary named by FCLONES_BIN with flags.txt and
// fails only when an option was added or removed. With UPDATE_GOLDEN=1 it
// rewrites flags.txt and the committed help texts instead.
func TestBinaryMatchesSnapshot(t *testing.T) {
	bin := os.Getenv("FCLONES_BIN")
	if bin == "" {
		t.Skip("FCLONES_BIN is unset; the image build sets it to the fclones binary it ships")
	}
	texts := map[string]string{}
	got, err := inventory(func(args ...string) (string, error) {
		out, err := exec.CommandContext(t.Context(), bin, args...).Output()
		texts[fixtureName(args)] = string(out)
		return string(out), err
	})
	if err != nil {
		t.Fatalf("read the options of %s: %v", bin, err)
	}
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		writeGolden(t, got, texts)
		return
	}
	added, removed := diff(Snapshot(), got)
	if len(added)+len(removed) == 0 {
		return
	}
	version, _ := exec.CommandContext(t.Context(), bin, "--version").Output()
	t.Fatalf("the options of %s differ from internal/fclonesflags/flags.txt.\n"+
		"added:%s\nremoved:%s\n"+
		"Add an added option to dangerousFlags in config.go when it runs a command or changes files in place, "+
		"and drop a removed one from it, then regenerate flags.txt with:\n  %s",
		strings.TrimSpace(string(version)), bullets(added), bullets(removed), regenHint)
}

func bullets(entries []string) string {
	if len(entries) == 0 {
		return " none"
	}
	return "\n  " + strings.Join(entries, "\n  ")
}

func writeGolden(t *testing.T, entries []string, texts map[string]string) {
	t.Helper()
	header := "Options of the pinned fclones, one \"<command> <flag>\" per line, read from its --help.\n" +
		"Generated; regenerate with: " + regenHint
	if err := os.WriteFile("flags.txt", []byte(formatList(header, entries)), 0o600); err != nil {
		t.Fatal(err)
	}
	stale, err := filepath.Glob(filepath.Join(helpDir, "*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range stale {
		if err := os.Remove(f); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(helpDir, 0o750); err != nil {
		t.Fatal(err)
	}
	for name, text := range texts {
		if err := os.WriteFile(filepath.Join(helpDir, name+".txt"), []byte(trimLines(text)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// trimLines drops trailing blanks clap pads lines with, so the committed
// texts follow the repo's editorconfig; option parsing ignores them.
func trimLines(text string) string {
	var b strings.Builder
	for line := range strings.Lines(text) {
		b.WriteString(strings.TrimRight(line, " \t\n") + "\n")
	}
	return b.String()
}
