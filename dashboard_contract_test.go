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
		Multi       bool   `json:"multi"`
		IncludeAll  bool   `json:"includeAll"`
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
func variableQuery(v *dashVariable) string {
	var q struct {
		Spec struct {
			Query string `json:"query"`
		} `json:"spec"`
	}
	if v.Kind != "QueryVariable" || json.Unmarshal(v.Spec.Query, &q) != nil {
		return ""
	}
	return q.Spec.Query
}

// dashboardQueries is every query variable's and panel query's PromQL, keyed
// by where it sits.
func dashboardQueries(d *dashboard) map[string]string {
	queries := map[string]string{}
	for name, v := range d.variables() {
		if q := variableQuery(&v); q != "" {
			queries["variable "+name] = q
		}
	}
	for name, p := range d.Spec.Elements {
		for i, e := range panelExprs(&p) {
			queries[fmt.Sprintf("%s %s query %d", name, p.Spec.Title, i)] = e
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
	alertsRe          = regexp.MustCompile(`ALERTS\{alertname="([A-Za-z]+)"([^}]*)\}`)
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
// series fails the build instead of blanking a panel. ALERTS is the ruler's
// series and instance the scrape's label.
func TestDashboardReadsOnlySeriesTheSchedulerServes(t *testing.T) {
	served := servedSeries(t)
	d := loadDashboard(t)
	for where, expr := range dashboardQueries(&d) {
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

// TestDashboardScopesEveryQueryToTheInstance fails when a query reads a series
// without the instance variable, or matches a label the user's own scrape or
// log config names (job, hostname, container), so the dashboard works under
// any job name and shows only the picked scheduler.
func TestDashboardScopesEveryQueryToTheInstance(t *testing.T) {
	d := loadDashboard(t)
	inst := d.variables()["instance"]
	if q := variableQuery(&inst); q != "label_values("+metricsPrefix+"_build_info, instance)" ||
		inst.Spec.Sort != "alphabeticalAsc" || inst.Spec.Refresh != "onTimeRangeChanged" ||
		inst.Spec.Multi || inst.Spec.IncludeAll || inst.Spec.Current.Text != "" || inst.Spec.Current.Value != "" {
		t.Errorf("instance variable reads %q sort %q refresh %q, want a single-select label_values over build_info, sorted alphabeticalAsc, refreshed onTimeRangeChanged, saved empty",
			q, inst.Spec.Sort, inst.Spec.Refresh)
	}
	for where, expr := range dashboardQueries(&d) {
		for _, sel := range selectorRe.FindAllStringSubmatch(expr, -1) {
			scoped := false
			for _, m := range matcherRe.FindAllStringSubmatch(sel[2], -1) {
				switch m[1] {
				case "job", "hostname", "container":
					t.Errorf("%s matches %s on %s, a label the user's own config names", where, m[1], sel[1])
				case "instance":
					scoped = m[2] == `=` && m[3] == "$instance" ||
						m[2] == `=~` && slices.Contains(strings.Split(m[3], "|"), "$instance")
				}
			}
			if !scoped {
				t.Errorf("%s reads %s without instance=\"$instance\"", where, sel[1])
			}
		}
		// The instance variable lists every instance, so it alone reads unscoped.
		if bare := seriesIn(selectorRe.ReplaceAllString(expr, " ")); where != "variable instance" && len(bare) > 0 {
			t.Errorf("%s reads %v without a label matcher", where, bare)
		}
	}
}

// TestDashboardAttentionTilesMirrorEveryShippedRule fails when a rule in
// alerts/promql.yaml has no Needs attention tile, or a tile names a rule the
// file does not hold. Each tile reads its rule's firing ALERTS series, names
// the rule, and starts hidden until its query answers. A rule with an absent()
// arm fires with no instance label, so its tile also matches an empty one.
func TestDashboardAttentionTilesMirrorEveryShippedRule(t *testing.T) {
	d := loadDashboard(t)
	rules := ruleExprs(t, promqlRules)
	attention := row(t, &d, "Needs attention")
	gate := attention.Spec.ConditionalRendering
	if gate == nil || gate.Spec.Visibility != "show" || len(gate.Spec.Items) != 1 ||
		gate.Spec.Items[0].Kind != "ConditionalRenderingVariable" ||
		gate.Spec.Items[0].Spec.Operator != "matches" || gate.Spec.Items[0].Spec.Value != "^[1-9]" {
		t.Fatal("Needs attention is not shown by a variable matching ^[1-9]")
	}
	gateVar := d.variables()[gate.Spec.Items[0].Spec.Variable]
	counted := variableQuery(&gateVar)
	var mirrored []string
	for _, it := range attention.Spec.Layout.Spec.Items {
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
		for _, e := range panelExprs(&p) {
			if !strings.Contains(counted, "count("+e+")") {
				t.Errorf("%q query is not counted by the row's variable", p.Spec.Title)
			}
			for _, m := range alertsRe.FindAllStringSubmatch(e, -1) {
				if !strings.Contains(m[2], `alertstate="firing"`) {
					t.Errorf("%q reads %s alerts in any state, want only firing ones", p.Spec.Title, m[1])
				}
				if !strings.Contains(p.Spec.Description, m[1]) {
					t.Errorf("%q reads %s, which its description does not name", p.Spec.Title, m[1])
				}
				if strings.Contains(rules[m[1]], "absent(") && !strings.Contains(m[2], `instance=~"$instance|"`) {
					t.Errorf("%q reads %s, whose absent() arm carries no instance, without instance=~\"$instance|\"", p.Spec.Title, m[1])
				}
				if !slices.Contains(mirrored, m[1]) {
					mirrored = append(mirrored, m[1])
				}
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(rules)) {
		if !slices.Contains(mirrored, name) {
			t.Errorf("%s has no Needs attention tile reading its firing alerts", name)
		}
	}
	for _, name := range mirrored {
		if _, ok := rules[name]; !ok {
			t.Errorf("a tile mirrors %s, which %s does not hold", name, promqlRules)
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
