package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/go/health/v2"
)

const (
	dashboardFile = "grafana-dashboard.json"
	promqlRules   = "alerts/promql.yaml"
)

type dashQuery struct {
	Spec struct {
		Query struct {
			Spec struct {
				Expr         string `json:"expr"`
				LegendFormat string `json:"legendFormat"`
			} `json:"spec"`
		} `json:"query"`
	} `json:"spec"`
}

type valueMapping struct {
	Options map[string]struct {
		Text string `json:"text"`
	} `json:"options"`
}

type dashPanel struct {
	Spec struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		Data        struct {
			Spec struct {
				Queries      []dashQuery `json:"queries"`
				QueryOptions struct {
					MaxDataPoints int `json:"maxDataPoints"`
				} `json:"queryOptions"`
			} `json:"spec"`
		} `json:"data"`
		VizConfig struct {
			Group string `json:"group"`
			Spec  struct {
				FieldConfig struct {
					Defaults struct {
						NoValue  string         `json:"noValue"`
						Mappings []valueMapping `json:"mappings"`
					} `json:"defaults"`
					Overrides []struct {
						Matcher struct {
							Options string `json:"options"`
						} `json:"matcher"`
						Properties []struct {
							ID    string `json:"id"`
							Value any    `json:"value"`
						} `json:"properties"`
					} `json:"overrides"`
				} `json:"fieldConfig"`
			} `json:"spec"`
		} `json:"vizConfig"`
	} `json:"spec"`
}

type conditionRule struct {
	Kind string `json:"kind"`
	Spec struct {
		Value    any    `json:"value"`
		Variable string `json:"variable"`
		Operator string `json:"operator"`
	} `json:"spec"`
}

type conditionGroup struct {
	Spec struct {
		Visibility string          `json:"visibility"`
		Condition  string          `json:"condition"`
		Items      []conditionRule `json:"items"`
	} `json:"spec"`
}

type gridItem struct {
	Spec struct {
		ConditionalRendering *conditionGroup `json:"conditionalRendering"`
		Element              struct {
			Name string `json:"name"`
		} `json:"element"`
	} `json:"spec"`
}

type layoutNode struct {
	Kind string `json:"kind"`
	Spec struct {
		Layout               *layoutNode     `json:"layout"`
		ConditionalRendering *conditionGroup `json:"conditionalRendering"`
		Title                string          `json:"title"`
		Rows                 []layoutNode    `json:"rows"`
		Items                []gridItem      `json:"items"`
		HideHeader           bool            `json:"hideHeader"`
	} `json:"spec"`
}

type dashVariable struct {
	Kind string `json:"kind"`
	Spec struct {
		Name        string `json:"name"`
		Hide        string `json:"hide"`
		Refresh     string `json:"refresh"`
		Regex       string `json:"regex"`
		Sort        string `json:"sort"`
		SkipURLSync bool   `json:"skipUrlSync"`
		Current     struct {
			Text  any `json:"text"`
			Value any `json:"value"`
		} `json:"current"`
		// Query is a data query on a QueryVariable and a string on a
		// CustomVariable.
		Query json.RawMessage `json:"query"`
	} `json:"spec"`
}

type dashboard struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		Title     string               `json:"title"`
		Elements  map[string]dashPanel `json:"elements"`
		Layout    layoutNode           `json:"layout"`
		Variables []dashVariable       `json:"variables"`
		Preload   bool                 `json:"preload"`
	} `json:"spec"`
}

func loadDashboard(t *testing.T) dashboard {
	t.Helper()
	raw, err := os.ReadFile(dashboardFile)
	if err != nil {
		t.Fatalf("reading %s: %v", dashboardFile, err)
	}
	var d dashboard
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("decoding %s: %v", dashboardFile, err)
	}
	if len(d.Spec.Elements) == 0 {
		t.Fatalf("%s holds no panel", dashboardFile)
	}
	return d
}

func panelExprs(p *dashPanel) []string {
	var out []string
	for _, q := range p.Spec.Data.Spec.Queries {
		out = append(out, q.Spec.Query.Spec.Expr)
	}
	return out
}

func (d *dashboard) variables() map[string]dashVariable {
	out := map[string]dashVariable{}
	for _, v := range d.Spec.Variables {
		out[v.Spec.Name] = v
	}
	return out
}

// variableQuery is a query variable's PromQL, or "" for any other variable.
// Grafana runs the query's __legacyStringValue, null as "", when that key is
// present (getDataQueryForVariable), and its query field otherwise.
func variableQuery(v *dashVariable) string {
	var q struct {
		Spec map[string]json.RawMessage `json:"spec"`
	}
	if v.Kind != "QueryVariable" || json.Unmarshal(v.Spec.Query, &q) != nil {
		return ""
	}
	raw, ok := q.Spec["__legacyStringValue"]
	if !ok {
		raw = q.Spec["query"]
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

// TestVariableQueryReadsTheQueryGrafanaRuns pins variableQuery to the query
// Grafana runs, so a pin on query cannot pass while __legacyStringValue holds
// another one.
func TestVariableQueryReadsTheQueryGrafanaRuns(t *testing.T) {
	for _, tc := range []struct{ name, spec, want string }{
		{"query only", `{"query":"up"}`, "up"},
		{"legacy wins", `{"query":"up","__legacyStringValue":"down"}`, "down"},
		{"legacy null is empty", `{"query":"up","__legacyStringValue":null}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := dashVariable{Kind: "QueryVariable"}
			v.Spec.Query = json.RawMessage(`{"spec":` + tc.spec + `}`)
			if got := variableQuery(&v); got != tc.want {
				t.Errorf("variableQuery = %q, want %q", got, tc.want)
			}
		})
	}
}

// dashboardQueries is every query variable's and panel query's PromQL, keyed
// by where it sits, with its # comments removed so no check reads a comment as
// a query.
func dashboardQueries(d *dashboard) map[string]string {
	queries := map[string]string{}
	for name, v := range d.variables() {
		if q := variableQuery(&v); q != "" {
			queries["variable "+name] = withoutComments(q)
		}
	}
	for name, p := range d.Spec.Elements {
		for i, e := range panelExprs(&p) {
			queries[fmt.Sprintf("%s %s query %d", name, p.Spec.Title, i)] = withoutComments(e)
		}
	}
	return queries
}

func row(t *testing.T, d *dashboard, title string) *layoutNode {
	t.Helper()
	for i := range d.Spec.Layout.Spec.Rows {
		if r := &d.Spec.Layout.Spec.Rows[i]; r.Spec.Title == title {
			return r
		}
	}
	t.Fatalf("no row titled %q", title)
	return nil
}

func panelTitled(t *testing.T, d *dashboard, title string) *dashPanel {
	t.Helper()
	var found []dashPanel
	for _, p := range d.Spec.Elements {
		if p.Spec.Title == title {
			found = append(found, p)
		}
	}
	if len(found) != 1 {
		t.Fatalf("found %d panels titled %q, want 1", len(found), title)
	}
	return &found[0]
}

// servedSeries builds the registry the way serve does and returns each series
// it serves with each label's values. Every labelled family is seeded at
// startup, so each label value a query may name is on the page.
func servedSeries(t *testing.T) map[string]map[string][]string {
	t.Helper()
	reg, _ := newReporter(&config{}, health.NewStatus())
	families, err := reg.Gather(t.Context())
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	served := map[string]map[string][]string{}
	for _, f := range families {
		if len(f.Samples) == 0 {
			continue
		}
		labels := map[string][]string{}
		for _, s := range f.Samples {
			for _, l := range s.Labels {
				if !slices.Contains(labels[l.Name], l.Value) {
					labels[l.Name] = append(labels[l.Name], l.Value)
				}
			}
		}
		served[f.Name] = labels
	}
	if _, ok := served[metricsPrefix+"_last_run_result"]; !ok {
		t.Fatalf("Setup: the registry serves no %s_last_run_result: %v", metricsPrefix, served)
	}
	return served
}

var (
	quotedRe          = regexp.MustCompile(`"(?:[^"\\]|\\.)*"`)
	selectorRe        = regexp.MustCompile(`([a-zA-Z_:][a-zA-Z0-9_:]*)\{([^}]*)\}`)
	matcherRe         = regexp.MustCompile(`([a-zA-Z_]\w*)\s*(=~|!~|!=|=)\s*"([^"]*)"`)
	matchersRe        = regexp.MustCompile(`\{[^}]*\}`)
	dashGroupingRe    = regexp.MustCompile(`\b(by|without|on|ignoring|group_left|group_right)\s*\(([^)]*)\)`)
	variableRe        = regexp.MustCompile(`\$\{?\w+(?::\w+)?\}?`)
	labelValuesRe     = regexp.MustCompile(`label_values\(([^,]+),\s*\w+\)`)
	functionRe        = regexp.MustCompile(`\b[a-z_]+\s*\(`)
	dashIdentRe       = regexp.MustCompile(`\b[a-zA-Z_:][a-zA-Z0-9_:]*\b`)
	alertsRe          = regexp.MustCompile(`ALERTS\{alertname="(\w+)"`)
	legendLabelRe     = regexp.MustCompile(`\{\{\s*(\w+)\s*\}\}`)
	lastRunResultCode = regexp.MustCompile(`(?:(\d+) \* )?sum\(` + metricsPrefix + `_last_run_result\{[^}]*result="(\w+)"\}\)`)
)

var promqlKeywords = []string{"and", "or", "unless", "bool", "offset"}

// seriesIn returns the series names a panel or variable query reads;
// label_values names a label after its series, which is no series.
func seriesIn(expr string) []string {
	expr = labelValuesRe.ReplaceAllString(expr, "$1")
	for _, re := range []*regexp.Regexp{matchersRe, quotedRe, dashGroupingRe, promRangeRe, variableRe, functionRe} {
		expr = re.ReplaceAllString(expr, " ")
	}
	var names []string
	for _, id := range dashIdentRe.FindAllString(expr, -1) {
		if !slices.Contains(promqlKeywords, id) && !slices.Contains(names, id) {
			names = append(names, id)
		}
	}
	return names
}

func TestDashboardIsASchemaV2Dashboard(t *testing.T) {
	d := loadDashboard(t)
	if d.APIVersion != "dashboard.grafana.app/v2" || d.Kind != "Dashboard" {
		t.Errorf("apiVersion %q kind %q, want dashboard.grafana.app/v2 Dashboard", d.APIVersion, d.Kind)
	}
	if d.Metadata.Name != "docker-fclones-scheduler" {
		t.Errorf("metadata.name = %q, want docker-fclones-scheduler, the name an import replaces in place", d.Metadata.Name)
	}
	if d.Spec.Title != "fclones Scheduler" {
		t.Errorf("title = %q, want fclones Scheduler", d.Spec.Title)
	}
}

// TestDashboardReadsOnlySeriesTheSchedulerServes fails when a query reads a
// series, a label or a label value the registry does not serve, so a renamed
// series fails the build instead of blanking a panel. It also fails a selector
// with no series name before its braces, which this test and the ALERTS pin
// cannot read. ALERTS is the ruler's series and instance the scrape's label.
func TestDashboardReadsOnlySeriesTheSchedulerServes(t *testing.T) {
	served := servedSeries(t)
	d := loadDashboard(t)
	for where, expr := range dashboardQueries(&d) {
		for _, sel := range selectors(bracedVariableRe.ReplaceAllString(expr, "$$v")) {
			if sel.name == "" {
				t.Errorf("%s has a selector with no series name before its braces, which this check cannot read: %s", where, sel.text)
			}
		}
		var labels []string
		for _, name := range seriesIn(expr) {
			switch have, ok := served[name]; {
			case name == "ALERTS":
			case !ok:
				t.Errorf("%s reads %s, which the scheduler does not serve", where, name)
			default:
				labels = append(labels, slices.Collect(maps.Keys(have))...)
			}
		}
		for _, sel := range selectorRe.FindAllStringSubmatch(expr, -1) {
			have, ok := served[sel[1]]
			if !ok {
				continue
			}
			for _, m := range matcherRe.FindAllStringSubmatch(sel[2], -1) {
				values, known := have[m[1]]
				switch {
				case m[1] == "instance":
				case !known:
					t.Errorf("%s matches %s on %s, which carries no such label", where, m[1], sel[1])
				case !strings.HasPrefix(m[3], "$") && !slices.Contains(values, m[3]):
					t.Errorf("%s matches %s=%q on %s, a value the scheduler never serves", where, m[1], m[3], sel[1])
				}
			}
		}
		for _, g := range dashGroupingRe.FindAllStringSubmatch(expr, -1) {
			for l := range strings.SplitSeq(g[2], ",") {
				l = strings.TrimSpace(l)
				// A label the query names as a string is one count_values or label_replace makes.
				computed := strings.Contains(expr, `"`+l+`"`)
				if l != "" && l != "instance" && !computed && !slices.Contains(labels, l) {
					t.Errorf("%s groups by %s, which no series it reads carries", where, l)
				}
			}
		}
	}
	for name, p := range d.Spec.Elements {
		for _, q := range p.Spec.Data.Spec.Queries {
			for _, m := range legendLabelRe.FindAllStringSubmatch(q.Spec.Query.Spec.LegendFormat, -1) {
				if m[1] == "instance" || !slices.ContainsFunc(seriesIn(q.Spec.Query.Spec.Expr), func(s string) bool {
					_, ok := served[s][m[1]]
					return ok
				}) {
					t.Errorf("%s %q legend shows %s, which does not vary inside one scheduler", name, p.Spec.Title, m[1])
				}
			}
		}
	}
}

// TestDashboardInstanceVariableListsEveryScheduler fails when the instance
// variable stops listing every scheduler that reports a build, sorted and
// refreshed with the time range, or is saved with a pick.
func TestDashboardInstanceVariableListsEveryScheduler(t *testing.T) {
	d := loadDashboard(t)
	inst := d.variables()["instance"]
	if q := variableQuery(&inst); q != "label_values("+metricsPrefix+"_build_info, instance)" ||
		inst.Spec.Sort != "alphabeticalAsc" || inst.Spec.Refresh != "onTimeRangeChanged" ||
		inst.Spec.Current.Text != "" || inst.Spec.Current.Value != "" {
		t.Errorf("instance variable reads %q sort %q refresh %q, want a label_values over build_info, sorted alphabeticalAsc, refreshed onTimeRangeChanged, saved empty",
			q, inst.Spec.Sort, inst.Spec.Refresh)
	}
}

// skipString returns the index just past the string literal opening at s[i].
func skipString(s string, i int) int {
	q := s[i]
	for i++; i < len(s); i++ {
		switch {
		case s[i] == '\\' && q != '`':
			i++
		case s[i] == q:
			return i + 1
		}
	}
	return len(s)
}

// withoutComments drops each # comment from expr, up to its newline. A # inside
// a string literal opens no comment, and a quote inside a comment opens no
// string.
func withoutComments(expr string) string {
	var b strings.Builder
	for i := 0; i < len(expr); {
		switch expr[i] {
		case '#':
			if j := strings.IndexByte(expr[i:], '\n'); j >= 0 {
				i += j
			} else {
				i = len(expr)
			}
		case '"', '\'', '`':
			j := skipString(expr, i)
			b.WriteString(expr[i:j])
			i = j
		default:
			b.WriteByte(expr[i])
			i++
		}
	}
	return b.String()
}

// bracedVariableRe matches ${name}, whose braces are no selector.
var bracedVariableRe = regexp.MustCompile(`\$\{\w+(?::\w+)?\}`)

type selectorBlock struct {
	name, text string
}

// selectors returns each {...} block of expr with the series name written
// before it, empty when none, a keyword (x or {...}) or a variable ($v{...})
// is. Quoted strings are skipped, so a brace inside a label value ends no
// block.
func selectors(expr string) []selectorBlock {
	var out []selectorBlock
	start := -1
	for i := 0; i < len(expr); i++ {
		switch c := expr[i]; {
		case c == '"' || c == '\'' || c == '`':
			i = skipString(expr, i) - 1
		case c == '{':
			start = i
		case c == '}' && start >= 0:
			before := strings.TrimRight(expr[:start], " ")
			name := before[len(strings.TrimRightFunc(before, func(r rune) bool {
				return r == '_' || r == ':' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
			})):]
			if slices.Contains(promqlKeywords, name) || strings.HasSuffix(before[:len(before)-len(name)], "$") {
				name = ""
			}
			out = append(out, selectorBlock{name: name, text: name + expr[start:i+1]})
			start = -1
		}
	}
	return out
}

// promqlCode is expr with its string literals and # comments blanked, so a
// function name inside one is not read as a call.
func promqlCode(expr string) string {
	b := []byte(expr)
	for i := 0; i < len(b); i++ {
		switch b[i] {
		case '"', '\'', '`':
			end := skipString(expr, i)
			for j := i; j < end; j++ {
				b[j] = ' '
			}
			i = end - 1
		case '#':
			for ; i < len(b) && b[i] != '\n'; i++ {
				b[i] = ' '
			}
		}
	}
	return string(b)
}

// absentCallRe matches a call of absent() or absent_over_time(); PromQL lets
// whitespace stand before a call's parenthesis.
var absentCallRe = regexp.MustCompile(`(?:^|[^\w:])absent(?:_over_time)?\s*\(`)

// attentionReads is every ALERTS selector a query may read the rule name's
// firing alerts with, for the rule expression expr. absent() and
// absent_over_time() label their alert with only the selector's equality
// matchers, and a shipped rule's selector names no instance, so only such a
// rule's alert is also read with instance="":
// https://prometheus.io/docs/prometheus/latest/querying/functions/#absent
func attentionReads(name, expr string) []string {
	read := `ALERTS{alertname="` + name + `", alertstate="firing", instance=`
	if absentCallRe.MatchString(promqlCode(expr)) {
		return []string{read + `"$instance"}`, read + `""}`}
	}
	return []string{read + `"$instance"}`}
}

// attentionTile is the query of the Needs attention tile for the rule name
// with expression expr, which answers 0 while any of its reads does.
func attentionTile(name, expr string) string {
	reads := attentionReads(name, expr)
	if len(reads) == 2 {
		return `0 * max((` + reads[0] + ` or ` + reads[1] + `))`
	}
	return `0 * max(` + reads[0] + `)`
}

// TestDashboardAttentionTilesMirrorEveryShippedRule fails when a rule in
// alerts/promql.yaml other than a severity: info notice has no Needs attention
// tile, a tile names a rule the file does not hold, a notice, or a rule its
// description does not name, a tile's query is not
// exactly attentionTile's for its rule, the row's variable does not count
// exactly those tiles in layout order, a tile is not hidden until its query
// answers, or any query reads an ALERTS selector that is not one of
// attentionReads' for a shipped rule.
func TestDashboardAttentionTilesMirrorEveryShippedRule(t *testing.T) {
	d := loadDashboard(t)
	rules := ruleExprs(t, promqlRules)
	notices := noticeRules(t, promqlRules)
	for where, expr := range dashboardQueries(&d) {
		for _, sel := range selectors(bracedVariableRe.ReplaceAllString(expr, "$$v")) {
			if sel.name != "ALERTS" {
				continue
			}
			name := ""
			if m := alertsRe.FindStringSubmatch(sel.text); m != nil {
				name = m[1]
			}
			if rule, ok := rules[name]; !ok || !slices.Contains(attentionReads(name, rule), sel.text) {
				t.Errorf("%s reads %s, which is none of the reads attentionReads gives a rule in %s: a looser instance match shows other schedulers' alerts, an unshipped alertname reads nothing the scheduler raises, and instance=\"\" reads nothing for a rule whose alerts carry instance", where, sel.text, promqlRules)
			}
		}
	}
	attention := row(t, &d, "Needs attention")
	gate := attention.Spec.ConditionalRendering
	if gate == nil || gate.Spec.Visibility != "show" || len(gate.Spec.Items) != 1 ||
		gate.Spec.Items[0].Kind != "ConditionalRenderingVariable" ||
		gate.Spec.Items[0].Spec.Operator != "matches" || gate.Spec.Items[0].Spec.Value != "^[1-9]" {
		t.Fatal("Needs attention is not shown by a variable matching ^[1-9]")
	}
	var mirrored, counts []string
	for i, it := range attention.Spec.Layout.Spec.Items {
		p := d.Spec.Elements[it.Spec.Element.Name]
		cr := it.Spec.ConditionalRendering
		var conds []string
		if cr != nil {
			for _, c := range cr.Spec.Items {
				conds = append(conds, c.Kind)
			}
		}
		slices.Sort(conds)
		if cr == nil || cr.Spec.Visibility != "show" || cr.Spec.Condition != "or" ||
			!slices.Equal(conds, []string{"ConditionalRenderingData", "ConditionalRenderingTimeRangeSize"}) ||
			p.Spec.Data.Spec.QueryOptions.MaxDataPoints <= 0 {
			t.Errorf("%q does not show exactly while its query answers: want a shown group or'ing a has-data rule with a 0s range rule, and a maxDataPoints pin", p.Spec.Title)
		}
		exprs := panelExprs(&p)
		if len(exprs) != 1 {
			t.Errorf("%q holds %d queries, want 1", p.Spec.Title, len(exprs))
			continue
		}
		counts = append(counts, fmt.Sprintf(`label_replace(count(%s), "tile", "%d", "", "")`, exprs[0], i+1))
		m := alertsRe.FindStringSubmatch(exprs[0])
		if m == nil {
			t.Errorf("%q reads no ALERTS series by alertname: %s", p.Spec.Title, exprs[0])
			continue
		}
		name := m[1]
		mirrored = append(mirrored, name)
		if !strings.Contains(p.Spec.Description, name) {
			t.Errorf("%q reads %s, which its description does not name", p.Spec.Title, name)
		}
		expr, ok := rules[name]
		if !ok {
			t.Errorf("a tile mirrors %s, which %s does not hold", name, promqlRules)
			continue
		}
		if notices[name] {
			t.Errorf("%q mirrors %s, a severity: info notice: Needs attention shows only alerts that need action", p.Spec.Title, name)
		}
		if want := attentionTile(name, expr); exprs[0] != want {
			t.Errorf("%q must read exactly\n%s\nso it shows only the picked scheduler's firing %s alerts; it reads\n%s",
				p.Spec.Title, want, name, exprs[0])
		}
	}
	gateVar := d.variables()[gate.Spec.Items[0].Spec.Variable]
	if got, want := variableQuery(&gateVar), "query_result(count("+strings.Join(counts, " or ")+") or vector(0))"; got != want {
		t.Errorf("the row's variable must count exactly its tiles in layout order:\n%s\nit reads\n%s", want, got)
	}
	for _, name := range slices.Sorted(maps.Keys(rules)) {
		if !notices[name] && !slices.Contains(mirrored, name) {
			t.Errorf("%s has no Needs attention tile reading its firing alerts", name)
		}
	}
}

// TestDashboardLastRunNamesEveryResult fails when a run result has no words
// of its own in the Last run tile or the Runs by result chart, so a new
// result shows as a bare number or a raw label value.
func TestDashboardLastRunNamesEveryResult(t *testing.T) {
	d := loadDashboard(t)
	lastRun := panelTitled(t, &d, "Last run")
	texts := map[string]string{}
	for _, m := range lastRun.Spec.VizConfig.Spec.FieldConfig.Defaults.Mappings {
		for value, o := range m.Options {
			texts[value] = o.Text
		}
	}
	byResult := map[string]string{}
	highest := 0
	for _, m := range lastRunResultCode.FindAllStringSubmatch(strings.Join(panelExprs(lastRun), "\n"), -1) {
		code := 1
		if m[1] != "" {
			code, _ = strconv.Atoi(m[1])
		}
		for other, c := range byResult {
			if c == strconv.Itoa(code) {
				t.Errorf("Last run gives %s and %s the same code %d", other, m[2], code)
			}
		}
		byResult[m[2]] = strconv.Itoa(code)
		highest = max(highest, code)
	}
	// After a restart only last_run_ok is known, read as the next two codes.
	expr := strings.Join(panelExprs(lastRun), "\n")
	for _, arm := range []string{
		fmt.Sprintf("%d + fclones_scheduler_last_run_ok{instance=\"$instance\"}", highest+1),
		`fclones_scheduler_last_run_timestamp_seconds{instance="$instance"} > 0`,
	} {
		if !strings.Contains(expr, arm) {
			t.Errorf("Last run lacks the restored-verdict arm %s", arm)
		}
	}
	for _, restored := range []int{highest + 1, highest + 2} {
		if texts[strconv.Itoa(restored)] == "" {
			t.Errorf("Last run gives the restored verdict %d no words", restored)
		}
	}
	var chart []string
	for _, o := range panelTitled(t, &d, "Runs by result").Spec.VizConfig.Spec.FieldConfig.Overrides {
		for _, prop := range o.Properties {
			if s, ok := prop.Value.(string); ok && prop.ID == "displayName" && s != "" {
				chart = append(chart, o.Matcher.Options)
			}
		}
	}
	for _, r := range runResults {
		code, ok := byResult[string(r)]
		if !ok || texts[code] == "" {
			t.Errorf("Last run gives the %s result no words", r)
		}
		if !slices.Contains(chart, string(r)) {
			t.Errorf("Runs by result gives the %s result no display name", r)
		}
	}
}

// TestDashboardScanFiguresWaitForAScan fails when a query reads a duplicate
// gauge without the scan-phase guard: those gauges restart at 0, and only a
// scan in this process sets them.
func TestDashboardScanFiguresWaitForAScan(t *testing.T) {
	d := loadDashboard(t)
	guard := " and on (instance) " + metricsPrefix + `_phase_duration_seconds{instance="$instance", phase="scan"} > 0`
	read := 0
	for where, expr := range dashboardQueries(&d) {
		if !slices.ContainsFunc(seriesIn(expr), func(s string) bool {
			return slices.Contains([]string{"duplicate_groups", "duplicate_files", "redundant_bytes"}, strings.TrimPrefix(s, metricsPrefix+"_"))
		}) {
			continue
		}
		read++
		if !strings.Contains(expr, guard) {
			t.Errorf("%s reads a duplicate gauge without%s", where, guard)
		}
	}
	if read == 0 {
		t.Error("no query reads a duplicate gauge")
	}
}

// TestDashboardRowHeadersFoldTwoPanels fails when a row shows a header over
// fewer than two always-shown panels, which costs a click and hides nothing.
// A row of tiles shown only with data needs two of them, since it lights
// several.
func TestDashboardRowHeadersFoldTwoPanels(t *testing.T) {
	d := loadDashboard(t)
	for _, r := range d.Spec.Layout.Spec.Rows {
		if r.Spec.HideHeader {
			continue
		}
		shown, gated := 0, 0
		for _, it := range r.Spec.Layout.Spec.Items {
			if it.Spec.ConditionalRendering == nil {
				shown++
			} else {
				gated++
			}
		}
		if shown < 2 && (shown > 0 || gated < 2) {
			t.Errorf("row %q shows a header over %d always-shown panels and %d shown only with data, want two of one kind or hideHeader",
				r.Spec.Title, shown, gated)
		}
	}
}

func TestDashboardPanelsExplainThemselves(t *testing.T) {
	d := loadDashboard(t)
	if !d.Spec.Preload {
		t.Error("spec.preload = false, want true so a hidden tile below the fold still queries")
	}
	if d.Spec.Layout.Kind != "RowsLayout" || len(d.Spec.Layout.Spec.Rows) == 0 {
		t.Errorf("layout is %s, want one RowsLayout", d.Spec.Layout.Kind)
	}
	for name, p := range d.Spec.Elements {
		if strings.TrimSpace(p.Spec.Description) == "" {
			t.Errorf("%s %q has no description", name, p.Spec.Title)
		}
		switch p.Spec.VizConfig.Group {
		case "stat", "gauge", "bargauge", "table":
			if strings.TrimSpace(p.Spec.VizConfig.Spec.FieldConfig.Defaults.NoValue) == "" {
				t.Errorf("%s %q has no no-value text", name, p.Spec.Title)
			}
		}
	}
}

const spanTitle = "${span} ${spanunit}"

// rangeWordVars are the variables a title names the range length with.
var rangeWordVars = []string{"span", "spanunit", "spanthe"}

// customText is the text Grafana gives a custom variable whose query is query,
// which titles render. The parse follows CustomVariable.transformCsvStringToOptions,
// https://github.com/grafana/scenes/blob/main/packages/scenes/src/variables/variants/CustomVariable.tsx.
// More than one option would let Grafana keep a stale choice, so it fails.
func customText(t *testing.T, query string) string {
	t.Helper()
	entries := regexp.MustCompile(`(?:\\,|[^,])+`).FindAllString(query, -1)
	if len(entries) != 1 {
		t.Errorf("custom query %q gives %d options, want 1", query, len(entries))
		return ""
	}
	if m := regexp.MustCompile(`^\s*(.+)\s:\s(.+)$`).FindStringSubmatch(entries[0]); m != nil {
		return strings.TrimSpace(m[1])
	}
	return strings.TrimSpace(entries[0])
}

// rangeWords is the text of each range word variable once spanread holds the
// option Grafana makes of row, or none at all (""), as on a failed first read.
func rangeWords(t *testing.T, vars map[string]dashVariable, row string) map[string]string {
	t.Helper()
	read := vars["spanread"]
	rx, err := regexp.Compile(strings.TrimSuffix(strings.TrimPrefix(read.Spec.Regex, "/"), "/"))
	if err != nil || rx.SubexpIndex("value") < 0 || rx.SubexpIndex("text") < 0 {
		t.Fatalf("spanread regex %q: %v, want value and text groups", read.Spec.Regex, err)
	}
	var value, text string
	if row != "" {
		m := rx.FindStringSubmatch(row)
		if m == nil {
			t.Fatalf("spanread regex %v does not match %s", rx, row)
		}
		value, text = m[rx.SubexpIndex("value")], m[rx.SubexpIndex("text")]
	}
	fill := strings.NewReplacer("${spanread:text}", text, "${spanread}", value)
	words := map[string]string{}
	for _, name := range rangeWordVars {
		var query string
		_ = json.Unmarshal(vars[name].Spec.Query, &query)
		words[name] = customText(t, fill.Replace(query))
	}
	return words
}

// panelNames are a panel's title and the display names its overrides give
// its fields, such as a table column's header.
func panelNames(p *dashPanel) []string {
	names := []string{p.Spec.Title}
	for _, o := range p.Spec.VizConfig.Spec.FieldConfig.Overrides {
		for _, prop := range o.Properties {
			if s, ok := prop.Value.(string); ok && prop.ID == "displayName" {
				names = append(names, s)
			}
		}
	}
	return names
}

// A figure over the time range reads only with the range's length, so every
// panel outside the charts whose query reads $__range names it through span
// and spanunit, in its title or a column header. Every range word is a custom
// variable over spanread's one answer, and reads "the range" until spanread
// has read a length.
func TestDashboardRangeFiguresNameTheRangeLength(t *testing.T) {
	d := loadDashboard(t)
	vars := d.variables()
	read, ok := vars["spanread"]
	if !ok {
		t.Fatal("variable spanread missing, want one read of the range length for every range word")
	}
	if read.Kind != "QueryVariable" || read.Spec.Hide != "hideVariable" || read.Spec.Refresh != "onTimeRangeChanged" || !read.Spec.SkipURLSync ||
		read.Spec.Current.Text != "" || read.Spec.Current.Value != "" {
		t.Errorf("spanread is %s hide %q refresh %q, want a hidden QueryVariable refreshed on time range change, saved empty, not in the URL",
			read.Kind, read.Spec.Hide, read.Spec.Refresh)
	}
	for _, name := range slices.Sorted(maps.Keys(vars)) {
		if name != "spanread" && strings.Contains(string(vars[name].Spec.Query), "$__range_s") {
			t.Errorf("variable %s reads $__range_s, want spanread alone to read the range length", name)
		}
	}
	for _, name := range rangeWordVars {
		if v := vars[name]; v.Kind != "CustomVariable" || v.Spec.Hide != "hideVariable" || !v.Spec.SkipURLSync {
			t.Errorf("variable %s is %q hide %q, want a hidden CustomVariable kept out of the URL", name, v.Kind, v.Spec.Hide)
		}
	}
	if got, want := rangeWords(t, vars, ""), map[string]string{"span": "the", "spanunit": "range", "spanthe": "the"}; !maps.Equal(got, want) {
		t.Errorf("range words before spanread reads a length are %v, want %v", got, want)
	}
	query := variableQuery(&read)
	for _, c := range []struct {
		span, unit string
		rangeS     int
	}{
		{"45", "seconds", 45},
		{"2", "minutes", 120},
		{"120", "minutes", 7199},
		{"2", "hours", 7200},
		{"5.6", "hours", 19980},
		{"48", "hours", 172799},
		{"2", "days", 172800},
		{"30", "days", 2592000},
	} {
		got := rangeWords(t, vars, queryResultRow(t, query, c.rangeS))
		if want := map[string]string{"span": c.span, "spanunit": c.unit, "spanthe": "the " + c.span}; !maps.Equal(got, want) {
			t.Errorf("range words at a %d s range are %v, want %v", c.rangeS, got, want)
		}
	}
	figures := 0
	for name, p := range d.Spec.Elements {
		if g := p.Spec.VizConfig.Group; g == "timeseries" || g == "state-timeline" {
			continue
		}
		if !slices.ContainsFunc(panelExprs(&p), func(e string) bool { return strings.Contains(e, "$__range") }) {
			continue
		}
		figures++
		if !slices.ContainsFunc(panelNames(&p), func(n string) bool { return strings.Contains(n, spanTitle) }) {
			t.Errorf("%s %q reads $__range, want its title or a column to name %s", name, p.Spec.Title, spanTitle)
		}
	}
	if figures == 0 {
		t.Error("no range figure found; the check no longer reads the dashboard")
	}
}

// TestDashboardQueriesAreTheTestedOnes fails when a panel query differs from
// the one alerts/promql_test.yaml evaluates under its anchor, so a change to
// its instance match or slot arithmetic runs through promtool in CI.
func TestDashboardQueriesAreTheTestedOnes(t *testing.T) {
	d := loadDashboard(t)
	raw, err := os.ReadFile("alerts/promql_test.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ title, anchor, instance string }{
		{"fclones Scheduler scrape", "down_tile", "[::1]:9469"},
		{"Space freed", "space_freed", "fc:9469"},
		{"Runs by result", "runs_by_result", "fc:9469"},
	} {
		t.Run(c.anchor, func(t *testing.T) {
			exprs := panelExprs(panelTitled(t, &d, c.title))
			if len(exprs) != 1 {
				t.Fatalf("%q has %d queries, want 1", c.title, len(exprs))
			}
			want := strings.NewReplacer(`$instance`, c.instance, `${__interval_ms}`, "300000",
				`$__interval`, "5m").Replace(exprs[0])
			m := regexp.MustCompile(`(?m)expr: &` + c.anchor + ` '([^']*)'$`).FindSubmatch(raw)
			if m == nil {
				t.Fatalf("alerts/promql_test.yaml has no expr anchored as %s", c.anchor)
			}
			if string(m[1]) != want {
				t.Errorf("alerts/promql_test.yaml tests\n%s\nthe %q panel reads, with its variables filled in,\n%s", m[1], c.title, want)
			}
		})
	}
}
