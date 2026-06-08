package args_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/fclones-wrapper/internal/args"
	"pgregory.net/rapid"
)

// assertArgs is a test helper that compares args.Parse output to expected values.
func assertArgs(t *testing.T, input string, want []string) {
	t.Helper()
	got, err := args.Parse(input)
	if err != nil {
		t.Fatalf("args.Parse(%q): unexpected error: %v", input, err)
	}
	if !slices.Equal(got, want) {
		t.Errorf("args.Parse(%q) = %v, want %v", input, got, want)
	}
}

func TestParseArgs(t *testing.T) {
	t.Parallel()
	t.Run("simple flags", func(t *testing.T) {
		t.Parallel()
		assertArgs(t, "--min-size 1024 --threads 4",
			[]string{"--min-size", "1024", "--threads", "4"})
	})
	t.Run("double and single quotes", func(t *testing.T) {
		t.Parallel()
		assertArgs(t, `--path "/some dir/with spaces" --name 'test file'`,
			[]string{"--path", "/some dir/with spaces", "--name", "test file"})
	})
	t.Run("empty string", func(t *testing.T) {
		t.Parallel()
		assertArgs(t, "", nil)
	})
	t.Run("single flag pair", func(t *testing.T) {
		t.Parallel()
		assertArgs(t, "--rf-over 1", []string{"--rf-over", "1"})
	})
	t.Run("escaped space", func(t *testing.T) {
		t.Parallel()
		assertArgs(t, `--path /some\ dir/test --flag`,
			[]string{"--path", "/some dir/test", "--flag"})
	})
	t.Run("tab separators", func(t *testing.T) {
		t.Parallel()
		assertArgs(t, "--flag1\t--flag2\t\t--flag3",
			[]string{"--flag1", "--flag2", "--flag3"})
	})
	t.Run("leading and trailing whitespace", func(t *testing.T) {
		t.Parallel()
		assertArgs(t, "  --flag1  --flag2  ",
			[]string{"--flag1", "--flag2"})
	})
	t.Run("unterminated double quote", func(t *testing.T) {
		t.Parallel()
		_, err := args.Parse(`--path "/unclosed`)
		if err == nil {
			t.Error("expected error for unterminated double quote")
		}
	})
	t.Run("unterminated single quote", func(t *testing.T) {
		t.Parallel()
		_, err := args.Parse(`--path '/unclosed`)
		if err == nil {
			t.Error("expected error for unterminated single quote")
		}
	})
	t.Run("trailing backslash", func(t *testing.T) {
		t.Parallel()
		_, err := args.Parse(`--path /test\`)
		if err == nil {
			t.Error("expected error for trailing backslash")
		}
	})
}

func TestParseArgsMixedQuotes(t *testing.T) {
	t.Parallel()
	got, err := args.Parse(`--path="/my dir" --name='hello world' plain`)
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
	t.Parallel()
	got, err := args.Parse(`hello\ world foo\\bar`)
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
	t.Parallel()
	got, err := args.Parse("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 args for empty input, got %d", len(got))
	}
}

func TestParseArgsTabSeparated(t *testing.T) {
	t.Parallel()
	got, err := args.Parse("a\tb\tc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d args, want 3", len(got))
	}
}

func TestParseArgsEscapedQuoteInQuotes(t *testing.T) {
	t.Parallel()
	got, err := args.Parse(`"hello\"world"`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{`hello"world`}
	if !slices.Equal(got, want) {
		t.Errorf("args.Parse escaped quote = %v, want %v", got, want)
	}
}

func TestParseArgsAdjacentQuotes(t *testing.T) {
	t.Parallel()
	got, err := args.Parse(`"hello"'world'`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"helloworld"}
	if !slices.Equal(got, want) {
		t.Errorf("args.Parse adjacent quotes = %v, want %v", got, want)
	}
}

func TestParseArgsOnlyWhitespace(t *testing.T) {
	t.Parallel()
	got, err := args.Parse("   \t  \t  ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("args.Parse whitespace-only = %v, want nil", got)
	}
}

func TestParseArgsEmptyQuotedString(t *testing.T) {
	t.Parallel()
	got, err := args.Parse(`""`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("args.Parse empty quotes = %v, want nil (empty quoted string produces no arg)", got)
	}
}

// --- Property-based tests ---

func TestProperty_ParseArgsSimpleInput(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		numTokens := rapid.IntRange(0, 10).Draw(rt, "numTokens")
		tokens := make([]string, numTokens)
		for i := range numTokens {
			tokens[i] = rapid.StringMatching(`[a-zA-Z0-9\-_./=]{1,20}`).Draw(rt, "token")
		}
		input := strings.Join(tokens, " ")

		got, err := args.Parse(input)
		if err != nil {
			rt.Fatalf("unexpected error for simple input %q: %v", input, err)
		}

		want := strings.Fields(input)
		if !slices.Equal(got, want) {
			rt.Fatalf("args.Parse(%q) = %v, want %v (strings.Fields)", input, got, want)
		}
	})
}

func TestProperty_ParseArgsQuotedContent(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		content := rapid.StringMatching(`[a-zA-Z0-9 _\-./]{1,30}`).Draw(rt, "content")
		input := `"` + content + `"`

		got, err := args.Parse(input)
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

func TestProperty_ParseArgsNeverPanics(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		input := rapid.String().Draw(rt, "input")
		args.Parse(input)
	})
}

func TestProperty_ParseArgsRoundTrip(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		numTokens := rapid.IntRange(0, 10).Draw(rt, "numTokens")
		tokens := make([]string, numTokens)
		for i := range numTokens {
			tokens[i] = rapid.StringMatching(`[a-zA-Z0-9\-_./=]{1,20}`).Draw(rt, "token")
		}
		input := strings.Join(tokens, " ")

		parsed, err := args.Parse(input)
		if err != nil {
			rt.Fatalf("args.Parse(%q): unexpected error: %v", input, err)
		}

		rejoined := strings.Join(parsed, " ")
		reparsed, err := args.Parse(rejoined)
		if err != nil {
			rt.Fatalf("args.Parse(%q) round-trip error: %v", rejoined, err)
		}

		if !slices.Equal(parsed, reparsed) {
			rt.Fatalf("args.Parse round-trip failed: %v → %q → %v", parsed, rejoined, reparsed)
		}
	})
}
