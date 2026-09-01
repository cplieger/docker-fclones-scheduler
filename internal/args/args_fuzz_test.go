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
			if strings.Contains(input, `"`) || strings.Contains(input, `'`) || strings.HasSuffix(input, `\`) {
				return
			}
			t.Fatalf("unexpected error for input %q: %v", input, err)
		}
		for i, tok := range result {
			if tok == "" {
				t.Fatalf("token %d is empty for input %q", i, input)
			}
		}
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

// FuzzParse_roundtrip asserts any token slice Parse emits can be re-expressed
// (backslash-escaping every rune) and re-parsed to the identical slice —
// exercising the full token space the bounded rapid generator in
// args_test.go cannot reach.
func FuzzParse_roundtrip(f *testing.F) {
	f.Add(`plain args`)
	f.Add(`"quoted value" 'single'`)
	f.Add(`escaped\ space`)
	f.Add(``)

	f.Fuzz(func(t *testing.T, input string) {
		tokens, err := Parse(input)
		if err != nil {
			return
		}
		// Re-express each token by escaping every rune; join with a single
		// unescaped space (Parse's separator).
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
