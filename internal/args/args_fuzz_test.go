package args

import (
	"strings"
	"testing"
)

func FuzzParse(f *testing.F) {
	f.Add(`simple args`)
	f.Add(`"double quoted"`)
	f.Add(`'single quoted'`)
	f.Add(`mixed "double" and 'single'`)
	f.Add(`escaped\ space`)
	f.Add(`trailing\`)
	f.Add(`"unterminated`)
	f.Add(`""`)
	f.Add(``)

	f.Fuzz(func(t *testing.T, input string) {
		result, err := Parse(input)
		if err != nil {
			// Errors are expected for unterminated quotes / trailing backslash
			if strings.Contains(input, `"`) || strings.Contains(input, `'`) || strings.HasSuffix(input, `\`) {
				return // valid error case
			}
			// If no quotes or trailing backslash, should not error
			t.Fatalf("unexpected error for input %q: %v", input, err)
		}
		// On success, no token should be empty
		for i, tok := range result {
			if tok == "" {
				t.Fatalf("token %d is empty for input %q", i, input)
			}
		}
		// All non-whitespace content from input should appear in tokens
		// (modulo quote/escape characters)
		joined := strings.Join(result, "")
		for _, r := range input {
			if r == '"' || r == '\'' || r == '\\' || r == ' ' || r == '\t' {
				continue
			}
			if !strings.ContainsRune(joined, r) {
				t.Fatalf("rune %q from input lost in output for input %q -> %v", string(r), input, result)
			}
		}
	})
}

// FuzzParse_roundtrip asserts that any token slice Parse emits can be
// re-expressed (by backslash-escaping every rune) and re-parsed back to the
// identical slice. Backslash escaping is the parser's universal literal
// mechanism -- it applies inside and outside quotes -- so this exercises the
// full token space (spaces, quotes, control chars) that the bounded rapid
// round-trip generator in args_test.go cannot reach.
func FuzzParse_roundtrip(f *testing.F) {
	f.Add(`plain args`)
	f.Add(`"quoted value" 'single'`)
	f.Add(`escaped\ space`)
	f.Add(``)

	f.Fuzz(func(t *testing.T, input string) {
		tokens, err := Parse(input)
		if err != nil {
			return // only round-trip inputs Parse accepts
		}
		// Re-express each token by escaping every rune; join tokens with a
		// single unescaped space (which Parse treats as a separator).
		var sb strings.Builder
		for i, tok := range tokens {
			if i > 0 {
				sb.WriteByte(' ')
			}
			for _, r := range tok {
				sb.WriteByte('\\')
				sb.WriteRune(r)
			}
		}
		reparsed, err := Parse(sb.String())
		if err != nil {
			t.Fatalf("re-parsing requoted %q failed: %v", sb.String(), err)
		}
		if len(reparsed) != len(tokens) {
			t.Fatalf("round-trip token count for input %q: %d tokens -> requote+reparse %d tokens (%v vs %v)",
				input, len(tokens), len(reparsed), tokens, reparsed)
		}
		for i := range tokens {
			if reparsed[i] != tokens[i] {
				t.Fatalf("round-trip token %d for input %q: got %q, want %q", i, input, reparsed[i], tokens[i])
			}
		}
	})
}
