package acceptance

// Temporary: diagnosing A75's cold-start flakes on CI's runners. Not merged.

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/seeddata"
)

type diskSample map[string][4]int64 // reads, sectors read, ms reading, writes

func readDisks(t *testing.T) diskSample {
	b, err := os.ReadFile("/proc/diskstats")
	if err != nil {
		t.Fatal(err)
	}
	out := diskSample{}
	whole := regexp.MustCompile(`^(sd[a-z]+|vd[a-z]+|nvme\d+n\d+)$`)
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 14 || !whole.MatchString(f[2]) {
			continue
		}
		n := func(i int) int64 { v, _ := strconv.ParseInt(f[i], 10, 64); return v }
		out[f[2]] = [4]int64{n(3), n(5), n(6), n(7)}
	}
	return out
}

func psiIO() int64 {
	b, err := os.ReadFile("/proc/pressure/io")
	if err != nil {
		return -1
	}
	m := regexp.MustCompile(`some .*total=(\d+)`).FindStringSubmatch(string(b))
	if m == nil {
		return -1
	}
	v, _ := strconv.ParseInt(m[1], 10, 64)
	return v
}

func diskDelta(a, b diskSample) string {
	var parts []string
	for dev, y := range b {
		x := a[dev]
		r, sec, ms, w := y[0]-x[0], y[1]-x[1], y[2]-x[2], y[3]-x[3]
		if r == 0 && w == 0 {
			continue
		}
		lat := 0.0
		if r > 0 {
			lat = float64(ms) / float64(r)
		}
		parts = append(parts, fmt.Sprintf("%s r=%d %.1fMB %.2fms/r w=%d", dev, r, float64(sec)/2048, lat, w))
	}
	return strings.Join(parts, "; ")
}

var pgLogLine = regexp.MustCompile(`(duration: [0-9.]+ ms\s+\S+ [^:]*:\s*.{0,40}|canceling statement due to statement timeout|checkpoint starting: \S+|automatic (vacuum|analyze)[^\n]{0,60})`)

func pgLogSince(container string, since time.Time) string {
	out, _ := exec.Command("docker", "logs", "--since", since.UTC().Format(time.RFC3339Nano), container).CombinedOutput()
	var parts []string
	for _, line := range strings.Split(string(out), "\n") {
		if m := pgLogLine.FindString(line); m != "" {
			m = strings.Join(strings.Fields(m), " ")
			m = strings.ReplaceAll(m, "canceling statement due to statement timeout", "TIMEOUT")
			parts = append(parts, m)
		}
	}
	return strings.Join(parts, " | ")
}

func TestA75Diag(t *testing.T) {
	if os.Getenv("PAGERARCHIVE_A75_DIAG") != "1" {
		t.Skip("diagnostic")
	}
	container := os.Getenv("PAGERARCHIVE_A75_CONTAINER")
	e := setup(t)
	start := time.Now()
	seeded, built, err := seeddata.Seed(e.ctx, e.st.Pool, seeddata.A75Pages)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("seeded in %s, indexes in %s, all in %s", seeded.Round(time.Second), built.Round(time.Second), time.Since(start).Round(time.Second))
	if os.Getenv("PAGERARCHIVE_A75_DIAG_CHECKPOINT") == "1" {
		t0 := time.Now()
		if _, err := e.st.Pool.Exec(e.ctx, `CHECKPOINT`); err != nil {
			t.Fatal(err)
		}
		t.Logf("CHECKPOINT in %s", time.Since(t0).Round(time.Millisecond))
	}
	for _, s := range []string{`ALTER SYSTEM SET log_min_duration_statement = 0`, `ALTER SYSTEM SET log_autovacuum_min_duration = 0`} {
		if _, err := e.st.Pool.Exec(e.ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	var stats string
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT coalesce(string_agg(x, ' '), '') FROM (SELECT unnest(most_common_elems::text::text[]) x FROM pg_stats WHERE tablename = 'messages_search_bigrams_idx' LIMIT 12) s`).Scan(&stats); err != nil {
		t.Logf("stats: %v", err)
	}
	t.Logf("bigram stats (first 12 common elems): %s", stats)
	seedEnd := time.Now()
	// Idle disk after the seed, 3 windows of 10 s.
	for i := 0; i < 3; i++ {
		a, p := readDisks(t), psiIO()
		time.Sleep(10 * time.Second)
		t.Logf("idle %d after the seed: %s; io stall %dms", i, diskDelta(a, readDisks(t)), (psiIO()-p)/1000)
	}
	searches := []struct{ name, alias, picked, text string }{
		{"nothing", "", "", "ZZNOTHING"},
		{"hoses grass pump", "", "", "hoses grass pump"},
		{"smoke or grass", "", "", `"Smoke showing" or:"grass fire"`},
		{"tanker9", "", "", "tanker9"},
		{"Station 1 12345", "Station 1", "", "12345"},
		{"Station 1 STN2", "Station 1", "", "STN2"},
		{"sta STN2", "sta", "", "STN2"},
		{"picked STN2", "", "Station 1", "STN2"},
		{"busy alias fire", "Station 5", "", "fire"},
		{"m 23 b4", "", "", "m 23 b4"},
		{"m 345 c7", "", "", "m 345 c7"},
	}
	rounds := 3
	if v := os.Getenv("PAGERARCHIVE_A75_DIAG_ROUNDS"); v != "" {
		rounds, _ = strconv.Atoi(v)
	}
	for round := 1; round <= rounds; round++ {
		for _, s := range searches {
			query := "q=" + url.QueryEscape(s.text)
			if s.alias != "" {
				query += "&alias=" + url.QueryEscape(s.alias)
			}
			if s.picked != "" {
				query += "&alias_like=" + url.QueryEscape(s.picked)
			}
			c0 := time.Now()
			a75ColdStart(t, e, container)
			restart := time.Since(c0)
			d0, p0 := readDisks(t), psiIO()
			t0 := time.Now()
			e.list(query)
			cold := time.Since(t0)
			d1, p1 := readDisks(t), psiIO()
			time.Sleep(200 * time.Millisecond)
			flag := ""
			if cold > 700*time.Millisecond {
				flag = " SLOW"
			}
			t.Logf("r%d +%3.0fs %-18s cold %5dms%s (restart %s) disk: %s; io stall %dms\n      pg: %s", round, t0.Sub(seedEnd).Seconds(), s.name,
				cold.Milliseconds(), flag, restart.Round(time.Millisecond), diskDelta(d0, d1), (p1-p0)/1000, pgLogSince(container, t0.Add(-50*time.Millisecond)))
		}
	}
}
