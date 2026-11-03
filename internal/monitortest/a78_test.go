package monitortest

import (
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"testing"
)

// A78 for the monitoring stack (R84): installed from nothing but the release's
// files, Grafana has its data sources, dashboards and alert rules, from
// upstream's image and the archive. scripts/monitoring-checks.sh runs this
// before it adds anything of its own to the folder.
func TestA78MonitoringInstalled(t *testing.T) {
	url := os.Getenv("PA_MON_GRAFANA_URL")
	if url == "" {
		t.Skip("PA_MON_GRAFANA_URL is not set: run scripts/monitoring-checks.sh")
	}
	get := func(path string, out any) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, url+path, nil)
		req.SetBasicAuth("admin", os.Getenv("PA_MON_GRAFANA_PASSWORD"))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: %d", path, resp.StatusCode)
		}
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
	titles := func(items []struct {
		Title string `json:"title"`
		Name  string `json:"name"`
	}) []string {
		var out []string
		for _, i := range items {
			out = append(out, i.Title+i.Name)
		}
		return out
	}
	var items []struct {
		Title string `json:"title"`
		Name  string `json:"name"`
	}
	get("/api/search?type=dash-db", &items)
	if got := titles(items); len(got) != 3 {
		t.Errorf("dashboards: %q, want the 3 from the archive", got)
	} else {
		t.Logf("A78: dashboards: %q", got)
	}
	items = nil
	get("/api/v1/provisioning/alert-rules", &items)
	got := titles(items)
	for _, rule := range []string{"Client not receiving", "Radio stalled", "API unreachable", "Backup failing"} {
		if !slices.Contains(got, rule) {
			t.Errorf("alert rules: %q, no %q", got, rule)
		}
	}
	t.Logf("A78: alert rules: %q", got)
	items = nil
	get("/api/datasources", &items)
	got = titles(items)
	if len(got) != 3 {
		t.Errorf("data sources: %q, want Tempo, Loki and Prometheus", got)
	}
	t.Logf("A78: data sources: %q", got)
}
