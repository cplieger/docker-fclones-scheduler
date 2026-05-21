package args

import "testing"

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
		// Parse must not panic on any input.
		_, _ = Parse(input)
	})
}
