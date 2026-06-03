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
