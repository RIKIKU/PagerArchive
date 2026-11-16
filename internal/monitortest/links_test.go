package monitortest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The provisioned dashboards, by uid.
var dashboards = []string{"pa-page-path", "pa-requests", "pa-clients", "pa-backups"}

// A113. Links to Explore keep the time range: every link the dashboards
// and the Prometheus data source add to Explore carries the dashboard's
// time range (${__from} and ${__to}), and the Tempo data source looks a
// trace up by its ID whatever Explore's range, so Grafana's own trace
// links (which open at "Last 1 hour") still find older traces. Each
// series link on a graph from traces, filled in as Grafana fills it in
// for each series the panel shows (or a made-up one when it shows none),
// is an Explore address whose range is the dashboard's and whose query
// Tempo runs; when the panel shows series, the link's query finds spans.
// The browser check (clicking each link) is in the PR that added this;
// this one runs in CI with no browser.
func TestA113LinksKeepTheTimeRange(t *testing.T) {
	s := newStack(t)

	var ds []struct {
		UID      string `json:"uid"`
		JSONData struct {
			ExemplarTraceIDDestinations []struct {
				URL           string `json:"url"`
				DatasourceUID string `json:"datasourceUid"`
			} `json:"exemplarTraceIdDestinations"`
			TraceQuery struct {
				TimeShiftEnabled bool `json:"timeShiftEnabled"`
			} `json:"traceQuery"`
		} `json:"jsonData"`
	}
	s.grafanaGet(t, "/api/datasources", &ds)
	for _, d := range ds {
		switch d.UID {
		case "tempo":
			if d.JSONData.TraceQuery.TimeShiftEnabled {
				t.Errorf("Tempo's traceQuery.timeShiftEnabled is on: a trace opened from a link at \"Last 1 hour\" is only looked for around that hour")
			}
		case "prometheus":
			if len(d.JSONData.ExemplarTraceIDDestinations) == 0 {
				t.Errorf("Prometheus has no exemplar destination")
			}
			for _, e := range d.JSONData.ExemplarTraceIDDestinations {
				if e.DatasourceUID != "" || !keepsRange(e.URL) {
					t.Errorf("Prometheus's exemplar link %+v: want a URL with ${__from} and ${__to}, not a data source link (which opens at \"Last 1 hour\")", e)
				}
			}
		}
	}

	to := time.Now()
	from := to.Add(-6 * time.Hour)
	checked := 0
	for _, uid := range dashboards {
		var d struct {
			Dashboard struct {
				Panels []panelJSON `json:"panels"`
			} `json:"dashboard"`
		}
		s.grafanaGet(t, "/api/dashboards/uid/"+uid, &d)
		for _, p := range d.Dashboard.Panels {
			for _, l := range p.links() {
				if strings.Contains(l.URL, "/explore") && !keepsRange(l.URL) {
					t.Errorf("%s / %q: link %q doesn't carry the time range", uid, p.Title, l.Title)
				}
			}
			if p.Datasource.Type != "tempo" || p.Type != "timeseries" {
				continue
			}
			links := p.FieldConfig.Defaults.Links
			if len(links) == 0 {
				t.Errorf("%s / %q: a graph from traces with no link to Explore", uid, p.Title)
				continue
			}
			series := s.tempoSeries(t, p.Targets, from, to)
			made := len(series) == 0
			if made {
				series = []map[string]string{madeUpLabels(p.Targets)}
			}
			for _, l := range links {
				for _, labels := range series {
					s.checkSeriesLink(t, uid, p.Title, l.URL, labels, from, to, !made)
					checked++
				}
			}
		}
	}
	t.Logf("A113: %d series links checked", checked)
}

type linkJSON struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

type panelJSON struct {
	Title      string `json:"title"`
	Type       string `json:"type"`
	Datasource struct {
		Type string `json:"type"`
	} `json:"datasource"`
	Targets     []json.RawMessage `json:"targets"`
	FieldConfig struct {
		Defaults struct {
			Links []linkJSON `json:"links"`
		} `json:"defaults"`
		Overrides []struct {
			Properties []struct {
				ID    string          `json:"id"`
				Value json.RawMessage `json:"value"`
			} `json:"properties"`
		} `json:"overrides"`
	} `json:"fieldConfig"`
}

// links is every data link the panel defines, its overrides' included.
func (p panelJSON) links() []linkJSON {
	out := append([]linkJSON(nil), p.FieldConfig.Defaults.Links...)
	for _, o := range p.FieldConfig.Overrides {
		for _, prop := range o.Properties {
			var ls []linkJSON
			if prop.ID == "links" && json.Unmarshal(prop.Value, &ls) == nil {
				out = append(out, ls...)
			}
		}
	}
	return out
}

func keepsRange(u string) bool {
	return strings.Contains(u, "${__from}") && strings.Contains(u, "${__to}")
}

// tempoSeries runs a TraceQL metrics panel's query over the range, as the
// panel does, and returns each series' labels as Grafana gives them to a
// link (a string's value in quotes, as TraceQL writes it).
func (s *stack) tempoSeries(t *testing.T, targets []json.RawMessage, from, to time.Time) []map[string]string {
	t.Helper()
	var q map[string]any
	if err := json.Unmarshal(targets[0], &q); err != nil {
		t.Fatal(err)
	}
	var out []map[string]string
	for _, f := range s.dsQuery(t, q, from, to) {
		for _, fl := range f.Schema.Fields {
			if len(fl.Labels) > 0 {
				out = append(out, fl.Labels)
			}
		}
	}
	return out
}

type frameJSON struct {
	Schema struct {
		Fields []struct {
			Name   string            `json:"name"`
			Labels map[string]string `json:"labels"`
		} `json:"fields"`
	} `json:"schema"`
	Data struct {
		Values [][]any `json:"values"`
	} `json:"data"`
}

// dsQuery runs one query through Grafana, as the admin.
func (s *stack) dsQuery(t *testing.T, q map[string]any, from, to time.Time) []frameJSON {
	t.Helper()
	q["refId"] = "A"
	q["intervalMs"] = 15000
	q["maxDataPoints"] = 1000
	body, _ := json.Marshal(map[string]any{"queries": []any{q}, "from": strconv.FormatInt(from.UnixMilli(), 10), "to": strconv.FormatInt(to.UnixMilli(), 10)})
	req, _ := http.NewRequest(http.MethodPost, s.grafana+"/api/ds/query", bytes.NewReader(body))
	req.SetBasicAuth("admin", s.grafanaPassword)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out struct {
		Results map[string]struct {
			Error  string      `json:"error"`
			Frames []frameJSON `json:"frames"`
		} `json:"results"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("query %v: %d %s", q["query"], resp.StatusCode, b)
	}
	if r := out.Results["A"]; r.Error != "" {
		t.Errorf("query %v: %s", q["query"], r.Error)
	}
	return out.Results["A"].Frames
}

// madeUpLabels are labels like the ones a panel's query groups by, for a
// panel with no series yet: a number for user IDs, a string otherwise.
func madeUpLabels(targets []json.RawMessage) map[string]string {
	var q struct {
		Query string `json:"query"`
	}
	_ = json.Unmarshal(targets[0], &q)
	labels := map[string]string{}
	if m := regexp.MustCompile(`by \(([^)]*)\)`).FindStringSubmatch(q.Query); m != nil {
		for _, k := range strings.Split(m[1], ",") {
			k = strings.TrimSpace(k)
			labels[k] = `"made-up value"`
			if k == "span.user.id" {
				labels[k] = "42"
			}
		}
	}
	return labels
}

var variable = regexp.MustCompile(`\$\{([^}]+)\}`)

// interpolate fills in a link's variables as Grafana does for a series:
// the range and the field's labels, each URI-encoded.
func interpolate(t *testing.T, u string, labels map[string]string, from, to time.Time) string {
	return variable.ReplaceAllStringFunc(u, func(v string) string {
		name := v[2 : len(v)-1]
		switch {
		case name == "__from":
			return strconv.FormatInt(from.UnixMilli(), 10)
		case name == "__to":
			return strconv.FormatInt(to.UnixMilli(), 10)
		case strings.HasPrefix(name, `__field.labels["`):
			return url.QueryEscape(labels[strings.TrimSuffix(strings.TrimPrefix(name, `__field.labels["`), `"]`)])
		case strings.HasPrefix(name, "__field.labels."):
			return url.QueryEscape(labels[strings.TrimPrefix(name, "__field.labels.")])
		}
		t.Errorf("link %q: variable %s isn't one a series link has", u, v)
		return v
	})
}

type filterJSON struct {
	Tag       string `json:"tag"`
	Scope     string `json:"scope"`
	Operator  string `json:"operator"`
	Value     any    `json:"value"`
	ValueType string `json:"valueType"`
}

// traceQL is the query Tempo's search editor makes of its filters.
func traceQL(filters []filterJSON) string {
	var parts []string
	for _, f := range filters {
		tag := f.Tag
		if f.Scope != "intrinsic" {
			tag = f.Scope + "." + tag
		}
		v := fmt.Sprint(f.Value)
		if f.ValueType == "string" {
			v = strconv.Quote(v)
		}
		parts = append(parts, tag+f.Operator+v)
	}
	return "{" + strings.Join(parts, " && ") + "}"
}

// checkSeriesLink fills in a series link, opens it as Explore reads it
// (its panes parameter), and runs its query over its range.
func (s *stack) checkSeriesLink(t *testing.T, uid, title, link string, labels map[string]string, from, to time.Time, wantSpans bool) {
	t.Helper()
	filled := interpolate(t, link, labels, from, to)
	u, err := url.Parse(filled)
	if err != nil {
		t.Errorf("%s / %q, series %v: %v", uid, title, labels, err)
		return
	}
	var panes map[string]struct {
		Datasource string `json:"datasource"`
		Queries    []struct {
			QueryType string       `json:"queryType"`
			Filters   []filterJSON `json:"filters"`
		} `json:"queries"`
		Range struct {
			From string `json:"from"`
			To   string `json:"to"`
		} `json:"range"`
	}
	if err := json.Unmarshal([]byte(u.Query().Get("panes")), &panes); err != nil {
		t.Errorf("%s / %q, series %v: the link's panes aren't JSON: %v\n%s", uid, title, labels, err, u.Query().Get("panes"))
		return
	}
	for _, p := range panes {
		if p.Range.From != strconv.FormatInt(from.UnixMilli(), 10) || p.Range.To != strconv.FormatInt(to.UnixMilli(), 10) {
			t.Errorf("%s / %q: Explore's range %v, want the dashboard's %d .. %d", uid, title, p.Range, from.UnixMilli(), to.UnixMilli())
		}
		if len(p.Queries) != 1 || p.Queries[0].QueryType != "traceqlSearch" {
			t.Errorf("%s / %q: queries %+v, want one Tempo search", uid, title, p.Queries)
			continue
		}
		q := traceQL(p.Queries[0].Filters)
		for k, v := range labels {
			if k == "p" { // the quantile, not a series label
				continue
			}
			if !strings.Contains(q, strings.TrimPrefix(k, "span.")) || !strings.Contains(q, strings.Trim(v, `"`)) {
				t.Errorf("%s / %q: query %s isn't filtered to the series' %s=%s", uid, title, q, k, v)
			}
		}
		frames := s.dsQuery(t, map[string]any{"datasource": map[string]string{"type": "tempo", "uid": "tempo"}, "queryType": "traceql", "tableType": "spans", "limit": 20, "query": q}, from, to)
		spans := 0
		for _, f := range frames {
			for _, fl := range f.Schema.Fields {
				if fl.Name == "spanID" && len(f.Data.Values) > 0 {
					spans += len(f.Data.Values[0])
				}
			}
		}
		if wantSpans && spans == 0 {
			t.Errorf("%s / %q, series %v: the link's query %s finds no spans in its range", uid, title, labels, q)
		}
		t.Logf("A113: %s / %q, series %v: Explore at %s .. %s with %s: %d spans", uid, title, labels, p.Range.From, p.Range.To, q, spans)
	}
}
