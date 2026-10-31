package telemetry_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/RIKIKU/PagerArchive/internal/telemetry"
)

// R70, R76: the example stack's Alloy keeps the same attribute keys the app
// exports (its second line of defence), so the two lists can't drift apart.
func TestAlloyAllowlistMatches(t *testing.T) {
	config, err := os.ReadFile(filepath.Join("..", "..", "deploy", "monitoring", "alloy", "config.alloy"))
	if err != nil {
		t.Fatal(err)
	}
	// keep_keys(span.attributes, ["a", "b"]): the context and its list.
	re := regexp.MustCompile(`keep_keys\((\w+)\.attributes, \[([^\]]*)\]\)`)
	lists := map[string][][]string{}
	for _, m := range re.FindAllStringSubmatch(string(config), -1) {
		var keys []string
		for _, q := range regexp.MustCompile(`"([^"]*)"`).FindAllStringSubmatch(m[2], -1) {
			keys = append(keys, q[1])
		}
		lists[m[1]] = append(lists[m[1]], keys)
	}

	var metric []string
	for _, k := range telemetry.MetricKeys {
		metric = append(metric, string(k))
	}
	want := []struct {
		context, name string
		keys          []string
		lists         int // how many keep_keys statements use it
	}{
		{"span", "SpanKeys", keysOf(telemetry.SpanKeys), 1},
		{"log", "LogKeys", keysOf(telemetry.LogKeys), 1},
		{"resource", "ResourceKeys", keysOf(telemetry.ResourceKeys), 3}, // traces, logs, metrics
		{"datapoint", "MetricKeys", metric, 1},
	}
	var summary []string
	for _, w := range want {
		got := lists[w.context]
		if len(got) != w.lists {
			t.Errorf("config.alloy has %d keep_keys(%s.attributes, ...), want %d", len(got), w.context, w.lists)
		}
		for i, keys := range got {
			missing, extra := diff(w.keys, keys)
			if len(missing) > 0 {
				t.Errorf("config.alloy's %s list %d lacks %s keys %q", w.context, i+1, w.name, missing)
			}
			if len(extra) > 0 {
				t.Errorf("config.alloy's %s list %d has %q, not in %s", w.context, i+1, extra, w.name)
			}
		}
		summary = append(summary, fmt.Sprintf("%s %d", w.name, len(w.keys)))
	}
	if t.Failed() {
		t.Logf("R70: config.alloy's allowlist DIFFERS from filter.go's: see the errors above")
	} else {
		t.Logf("R70: config.alloy keeps the same keys as filter.go (%s)", strings.Join(summary, ", "))
	}
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// diff is what's in want but not got, and in got but not want, sorted.
func diff(want, got []string) (missing, extra []string) {
	for _, k := range want {
		if !slices.Contains(got, k) {
			missing = append(missing, k)
		}
	}
	for _, k := range got {
		if !slices.Contains(want, k) {
			extra = append(extra, k)
		}
	}
	slices.Sort(missing)
	slices.Sort(extra)
	return missing, extra
}
