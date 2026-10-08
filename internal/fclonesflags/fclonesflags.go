// Package fclonesflags reads the options an fclones binary accepts from its
// --help output and holds the committed set the wrapper's argument gates were
// reviewed against (flags.txt). The image build compares the two, so a new
// fclones release that adds or removes an option stops the build until the
// option is reviewed.
package fclonesflags

import (
	_ "embed"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// rootCommand is the command name the top-level options are listed under.
const rootCommand = "fclones"

//go:embed flags.txt
var snapshot string

// clap prints an option entry at column 2 (a short flag, optionally followed
// by its long form, or a long flag when no option has a short one) or at
// column 6 (a long-only flag beside short ones). Wrapped description lines
// start deeper, so a "--name" quoted in a description never matches.
var optionLine = regexp.MustCompile(`^ {2}(?:(-[^\s,-])(?:, (--[A-Za-z0-9][A-Za-z0-9-]*))?|(?: {4})?(--[A-Za-z0-9][A-Za-z0-9-]*))(?:[ =\[]|$)`)

var commandLine = regexp.MustCompile(`^ {2}([a-z][a-z0-9-]*)(?:\s|$)`)

// Snapshot returns the committed "<command> <flag>" entries, sorted.
func Snapshot() []string {
	return parseList(snapshot)
}

// parseList reads "<command> <flag>" entries one per line, skipping blank
// lines and "#" comments, and returns them sorted and deduplicated.
func parseList(text string) []string {
	var entries []string
	for line := range strings.Lines(text) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		entries = append(entries, strings.Join(strings.Fields(line), " "))
	}
	slices.Sort(entries)
	return slices.Compact(entries)
}

// formatList renders entries in the form parseList reads, under a header
// comment, ready to be written as flags.txt.
func formatList(header string, entries []string) string {
	var b strings.Builder
	for line := range strings.Lines(header) {
		b.WriteString("# " + strings.TrimRight(line, "\n") + "\n")
	}
	for _, e := range entries {
		b.WriteString(e + "\n")
	}
	return b.String()
}

// options returns every flag spelling (short and long) listed in one help
// text, sorted and deduplicated. A "--flag=<VAL>" entry yields "--flag".
func options(help string) []string {
	var flags []string
	for line := range strings.Lines(help) {
		m := optionLine.FindStringSubmatch(strings.TrimRight(line, "\r\n"))
		if m == nil {
			continue
		}
		for _, f := range m[1:] {
			if f != "" {
				flags = append(flags, f)
			}
		}
	}
	slices.Sort(flags)
	return slices.Compact(flags)
}

// commands returns the subcommand names in the "Commands:" section of the
// top-level help, in listed order, without clap's own "help" entry.
func commands(help string) []string {
	var cmds []string
	inSection := false
	for line := range strings.Lines(help) {
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "Commands:":
			inSection = true
		case inSection && strings.TrimSpace(line) == "":
			return cmds
		case inSection:
			if m := commandLine.FindStringSubmatch(line); len(m) > 1 && m[1] != "help" {
				cmds = append(cmds, m[1])
			}
		}
	}
	return cmds
}

// helpFunc runs fclones with args and returns its standard output.
type helpFunc func(args ...string) (string, error)

// inventory reads the top-level help and the help of every subcommand it
// lists and returns the "<command> <flag>" entries, sorted. It fails when the
// top-level help lists no subcommand or a help text lists no option, because
// either means the help format changed and an empty result would hide it.
func inventory(help helpFunc) ([]string, error) {
	root, err := help("--help")
	if err != nil {
		return nil, fmt.Errorf("fclones --help: %w", err)
	}
	cmds := commands(root)
	if len(cmds) == 0 {
		return nil, errors.New("fclones --help lists no subcommand; the help format changed")
	}
	entries, err := scoped(rootCommand, root)
	if err != nil {
		return nil, err
	}
	for _, cmd := range cmds {
		text, err := help(cmd, "--help")
		if err != nil {
			return nil, fmt.Errorf("fclones %s --help: %w", cmd, err)
		}
		more, err := scoped(cmd, text)
		if err != nil {
			return nil, err
		}
		entries = append(entries, more...)
	}
	slices.Sort(entries)
	return entries, nil
}

func scoped(cmd, help string) ([]string, error) {
	flags := options(help)
	if len(flags) == 0 {
		return nil, fmt.Errorf("help of %q lists no option; the help format changed", cmd)
	}
	entries := make([]string, 0, len(flags))
	for _, f := range flags {
		entries = append(entries, cmd+" "+f)
	}
	return entries, nil
}

// diff returns the entries of got missing from want (added) and the entries
// of want missing from got (removed). Both inputs must be sorted.
func diff(want, got []string) (added, removed []string) {
	for _, e := range got {
		if _, found := slices.BinarySearch(want, e); !found {
			added = append(added, e)
		}
	}
	for _, e := range want {
		if _, found := slices.BinarySearch(got, e); !found {
			removed = append(removed, e)
		}
	}
	return added, removed
}
