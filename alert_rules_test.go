package main

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/go/health/v2"
)

// logqlRule is one rule of alerts/logql.yaml, read as text: the repo carries
// no YAML parser and the file's shape is fixed by its own header.
type logqlRule struct {
	name      string
	expr      string
	aggregate string
	field     string
	present   string
	op        string
	threshold float64
	window    time.Duration
}

var (
	unwrapRe    = regexp.MustCompile(`\| unwrap (\w+)`)
	presentRe   = regexp.MustCompile(`\| (\w+) != ""`)
	rangeRe     = regexp.MustCompile(`\[(\d+)h\]`)
	filterRe    = regexp.MustCompile("\\|= `([^`]+)`")
	aggregateRe = regexp.MustCompile(`(last_over_time|sum_over_time)\(`)
	compareRe   = regexp.MustCompile(`(==|>)\s*(\d+)\s*$`)
)

func readLogQLRules(t *testing.T) []logqlRule {
	t.Helper()
	raw, err := os.ReadFile("alerts/logql.yaml")
	if err != nil {
		t.Fatalf("reading the shipped LogQL rules: %v", err)
	}
	var rules []logqlRule
	for _, block := range strings.Split(string(raw), "- alert: ")[1:] {
		name, rest, _ := strings.Cut(block, "\n")
		_, expr, found := strings.Cut(rest, "expr: |\n")
		if !found {
			t.Fatalf("%s has no block expr", name)
		}
		expr, _, _ = strings.Cut(expr, "for:")
		expr = strings.TrimSpace(expr)
		rule := logqlRule{name: strings.TrimSpace(name), expr: expr}
		if m := unwrapRe.FindStringSubmatch(expr); m != nil {
			rule.field = m[1]
		}
		if m := presentRe.FindStringSubmatch(expr); m != nil {
			rule.present = m[1]
		}
		if m := aggregateRe.FindStringSubmatch(expr); m != nil {
			rule.aggregate = m[1]
		}
		if m := compareRe.FindStringSubmatch(expr); m != nil {
			rule.op = m[1]
			rule.threshold, _ = strconv.ParseFloat(m[2], 64)
		}
		if m := rangeRe.FindStringSubmatch(expr); m != nil {
			hours, _ := strconv.Atoi(m[1])
			rule.window = time.Duration(hours) * time.Hour
		}
		rules = append(rules, rule)
	}
	if len(rules) == 0 {
		t.Fatal("alerts/logql.yaml defines no rule")
	}
	return rules
}

func logqlRuleNamed(t *testing.T, name string) logqlRule {
	t.Helper()
	for _, r := range readLogQLRules(t) {
		if r.name == name {
			return r
		}
	}
	t.Fatalf("alerts/logql.yaml has no %s rule", name)
	return logqlRule{}
}

// TestLogQLRulesReadNumericRunFinishedFields binds every LogQL rule to the
// record: each selects lines by the record's message, and each field a rule
// unwraps is a number on every record that carries it. A rule with no
// presence filter unwraps a field every record carries, because Loki fails
// the query on a line that lacks it.
func TestLogQLRulesReadNumericRunFinishedFields(t *testing.T) {
	var records []map[string]string
	for i := range runPaths {
		if runPaths[i].completed {
			records = append(records, runThrough(t, &runPaths[i]).records...)
		}
	}
	wantFilter := `msg="` + runFinishedMsg + `"`
	for _, rule := range readLogQLRules(t) {
		t.Run(rule.name, func(t *testing.T) {
			filters := filterRe.FindAllStringSubmatch(rule.expr, -1)
			if len(filters) == 0 {
				t.Errorf("%s has no line filter", rule.name)
			}
			for _, m := range filters {
				if m[1] != wantFilter {
					t.Errorf("%s filters on %q, want %q", rule.name, m[1], wantFilter)
				}
			}
			if rule.field == "" {
				return
			}
			if rule.present != "" && rule.present != rule.field {
				t.Errorf("%s filters on %q being present but unwraps %q", rule.name, rule.present, rule.field)
			}
			carried := 0
			for _, rec := range records {
				value, ok := rec[rule.field]
				if !ok {
					if rule.present == "" {
						t.Errorf("%s unwraps %q, which the %s record lacks", rule.name, rule.field, rec["result"])
					}
					continue
				}
				carried++
				if _, err := strconv.ParseFloat(value, 64); err != nil {
					t.Errorf("%s unwraps %q, which a record carries as %q, not a number", rule.name, rule.field, value)
				}
			}
			if carried == 0 {
				t.Errorf("%s unwraps %q, which no run finished record carries", rule.name, rule.field)
			}
		})
	}
}

type timedRecord struct {
	fields map[string]string
	at     time.Duration
}

// evaluate runs a rule at `at` the way Loki does over one stream: the records
// inside the window that pass the rule's filter feed its range aggregation,
// and the rule fires when the result passes its comparison.
func evaluate(t *testing.T, rule logqlRule, records []timedRecord, at time.Duration) (value float64, firing bool) {
	t.Helper()
	if rule.aggregate == "" || rule.op == "" || rule.window <= 0 {
		t.Fatalf("%s is not a range aggregation over a window with a comparison: %s", rule.name, rule.expr)
	}
	seen := false
	for _, r := range records {
		if r.at > at || at-r.at >= rule.window {
			continue
		}
		raw, ok := r.fields[rule.field]
		if rule.present != "" && !ok {
			continue
		}
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			t.Fatalf("%s: field %q = %q: %v", rule.name, rule.field, raw, err)
		}
		if rule.aggregate == "sum_over_time" {
			value += v
		} else {
			value = v
		}
		seen = true
	}
	if !seen {
		return 0, false
	}
	if rule.op == "==" {
		return value, value == rule.threshold
	}
	return value, value > rule.threshold
}

// TestLogQLStateRulesHoldUntilTheNextGoodRun is each state rule's fixture,
// over records written by real runs: the problem fires the rule, a run that
// says nothing about it keeps it firing, and a good run inside the window
// resolves it.
func TestLogQLStateRulesHoldUntilTheNextGoodRun(t *testing.T) {
	good := recordFor(t, "action reclaims space")
	cases := []struct {
		rule    string
		bad     []string
		neutral string
	}{
		{rule: "FclonesRunFailed", bad: []string{
			"action exits non-zero", "action past its timeout", "action reclaims nothing",
			"scan exits non-zero", "undecodable report", "lock error",
		}},
		{rule: "FclonesFormatDrift", bad: []string{"action summary not recognized"}, neutral: "no duplicates"},
	}
	for _, c := range cases {
		rule := logqlRuleNamed(t, c.rule)
		for _, name := range c.bad {
			t.Run(c.rule+" after "+name, func(t *testing.T) {
				bad := recordFor(t, name)
				if _, firing := evaluate(t, rule, []timedRecord{{fields: bad}}, time.Hour); !firing {
					t.Errorf("%s does not fire one hour after the record %v", c.rule, bad)
				}
				if c.neutral != "" {
					held := []timedRecord{{fields: bad}, {fields: recordFor(t, c.neutral), at: time.Hour}}
					if _, firing := evaluate(t, rule, held, 2*time.Hour); !firing {
						t.Errorf("%s resolved on a %s run, which says nothing about it", c.rule, c.neutral)
					}
				}
				resolved := []timedRecord{{fields: bad}, {fields: good, at: time.Hour}}
				if _, firing := evaluate(t, rule, resolved, 2*time.Hour); firing {
					t.Errorf("%s still fires after a good run followed inside the window", c.rule)
				}
			})
		}
	}
}

// TestLogQLSpaceReclaimedCountsFilesInTheValue pins the reclaim notice: it
// fires after a run that processed files, carries the count as its value, and
// stays quiet for runs that processed none.
func TestLogQLSpaceReclaimedCountsFilesInTheValue(t *testing.T) {
	rule := logqlRuleNamed(t, "FclonesSpaceReclaimed")
	reclaimed := recordFor(t, "action reclaims space")
	value, firing := evaluate(t, rule, []timedRecord{{fields: reclaimed}, {fields: reclaimed, at: time.Hour}}, 2*time.Hour)
	if !firing || value != 2 {
		t.Errorf("after two runs that each processed one file: value %v firing %v, want 2 and firing", value, firing)
	}
	for _, name := range []string{"no duplicates", "action reclaims nothing", "action exits non-zero"} {
		if _, firing := evaluate(t, rule, []timedRecord{{fields: recordFor(t, name)}}, time.Hour); firing {
			t.Errorf("fires after a %s run, which processed no files", name)
		}
	}
	if strings.Contains(rule.expr, "by (files") {
		t.Errorf("%s groups by a count, so each count opens a new alert: %s", rule.name, rule.expr)
	}
}

// TestLogQLWindowsEqualTheHealthDeadline keeps every range equal to the
// deadman's, so a failed run is still the latest record when the deadman
// takes over, and the deadman equal to the healthcheck's deadline at the
// default settings.
func TestLogQLWindowsEqualTheHealthDeadline(t *testing.T) {
	deadline := health.Lease{Interval: defaultInterval, Cycles: 2, Timeout: defaultScanTimeout, Attempts: 2}.Duration()
	var deadman time.Duration
	for _, r := range readLogQLRules(t) {
		if strings.Contains(r.expr, "absent_over_time") {
			deadman = r.window
		}
	}
	if deadman != deadline {
		t.Fatalf("absent_over_time window = %s, want the healthcheck's %s", deadman, deadline)
	}
	for _, rule := range readLogQLRules(t) {
		if rule.window != deadman {
			t.Errorf("%s ranges over %s, want the deadman's %s", rule.name, rule.window, deadman)
		}
	}
}
