// Package args parses a shell-style argument string into a slice of tokens,
// honouring single and double quotes and backslash escapes.
package args

import (
	"fmt"
	"strings"
)

// parser holds the running state of Parse.
type parser struct {
	args      []string
	current   strings.Builder
	quoteChar rune
	inQuote   bool
	escaped   bool
}

// flushToken appends the current builder to args and resets it.
func (p *parser) flushToken() {
	if p.current.Len() > 0 {
		p.args = append(p.args, p.current.String())
		p.current.Reset()
	}
}

// step applies one input rune to the parser state.
func (p *parser) step(r rune) {
	if p.escaped {
		p.current.WriteRune(r)
		p.escaped = false
		return
	}
	if r == '\\' {
		p.escaped = true
		return
	}
	switch {
	case p.inQuote:
		if r == p.quoteChar {
			p.inQuote = false
			p.quoteChar = 0
			return
		}
		p.current.WriteRune(r)
	case r == '"' || r == '\'':
		p.inQuote = true
		p.quoteChar = r
	case r == ' ' || r == '\t':
		p.flushToken()
	default:
		p.current.WriteRune(r)
	}
}

// Parse splits a string into arguments respecting quotes (single and double).
// Returns an error if quotes are not properly terminated or a trailing backslash exists.
func Parse(input string) ([]string, error) {
	var p parser
	for _, r := range input {
		p.step(r)
	}
	if p.inQuote {
		return nil, fmt.Errorf("unterminated %c quote in: %s", p.quoteChar, input)
	}
	if p.escaped {
		return nil, fmt.Errorf("trailing backslash in: %s", input)
	}
	p.flushToken()
	return p.args, nil
}
