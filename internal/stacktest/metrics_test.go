package stacktest

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// lockedByFunction calls pagerarchive_locked_accounts() as the metrics
// role, the only thing that role may do in Authentik's database.
func lockedByFunction(t *testing.T, sql string) (string, error) {
	t.Helper()
	cmd := exec.Command("docker", "compose", "-f", "compose.yaml", "-f", "deploy/test/compose.yaml",
		"exec", "-T", "-e", "PGPASSWORD="+os.Getenv("PA_STACK_METRICS_DB_PASSWORD"), "db",
		"psql", "-qtA", "-v", "ON_ERROR_STOP=1", "-h", "127.0.0.1", "-U", "pagerarchive_metrics", "-d", "authentik", "-c", sql)
	cmd.Dir = "../.."
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (s *stack) scrape(t *testing.T) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, s.app.JoinPath("/metrics").String(), nil)
	req.Header.Set("Authorization", "Bearer "+os.Getenv("PA_STACK_METRICS_TOKEN"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics: %d %s", resp.StatusCode, b)
	}
	return string(b)
}

var lockedLine = regexp.MustCompile(`(?m)^pagerarchive_accounts_locked (\d+)$`)

// R52: the metrics count the accounts the guessing protection has locked
// (R44a), through a role that can call one function and read nothing else
// (ADR 0006, owner decision Q1).
func TestLockedAccountsMetric(t *testing.T) {
	s := setup(t, "restarted")
	o := s.owner(t)
	before, err := lockedByFunction(t, "SELECT pagerarchive_locked_accounts()")
	if err != nil {
		t.Fatalf("the metrics role can't call the function: %v %s", err, before)
	}
	if out, err := lockedByFunction(t, "SELECT count(*) FROM authentik_core_user"); err == nil || !strings.Contains(out, "permission denied") {
		t.Errorf("the metrics role read a table: %v %s", err, out)
	}
	if out, err := lockedByFunction(t, "SELECT count(*) FROM authentik_events_event"); err == nil || !strings.Contains(out, "permission denied") {
		t.Errorf("the metrics role read the event log: %v %s", err, out)
	}

	u := s.inviteAndEnrol(t, o, "metrics-lock@example.com")
	for i := 1; i <= wrongAttempts; i++ {
		s.passwordAttempt(t, u.email, fmt.Sprintf("wrong password %d", i))
	}
	n0, _ := strconv.Atoi(before)
	m := lockedLine.FindStringSubmatch(s.scrape(t))
	if m == nil {
		t.Fatal("no pagerarchive_accounts_locked in the metrics")
	}
	got, _ := strconv.Atoi(m[1])
	t.Logf("R52: accounts locked before %d, after 5 wrong passwords %d (scraped)", n0, got)
	if got != n0+1 {
		t.Errorf("metric %d after locking one more account, want %d", got, n0+1)
	}
	// 16 minutes later (shifted, as A33 does) the lock is over.
	s.shiftFailures(t, u.email, 16*time.Minute)
	after, err := lockedByFunction(t, "SELECT pagerarchive_locked_accounts()")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("R52: 16 minutes later the function counts %s", after)
	if after != before {
		t.Errorf("after the lock expired: %s, want %s", after, before)
	}
}
