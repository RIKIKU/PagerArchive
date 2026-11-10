package monitortest

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"testing"
	"time"
)

// A80's alert, as A65 does for a client: with the example stack scraping
// PagerArchive and a test contact point, a backup that fails with the database
// stopped fires "Backup failing" within 2 minutes, and the next good one
// resolves it. The rest of A80 is scripts/a80.sh.
func TestA80ToldWhenABackupFails(t *testing.T) {
	s := newStack(t)
	hook := &webhook{}
	l, err := net.Listen("tcp", os.Getenv("PA_MON_WEBHOOK_ADDR"))
	if err != nil {
		t.Fatal(err)
	}
	go http.Serve(l, hook)
	defer l.Close()

	// Wait for this backup, not any good one: the stack's own nightly backup
	// (BACKUP_AT, in the test stack's time zone) can run during the checks, and
	// Prometheus keeps its older time until the next scrape.
	backup := os.Getenv("PA_MON_BACKUP_NOW")
	started := time.Now().Unix()
	// One backup at a time: if the nightly one is running, this one is refused
	// until it's done.
	for end := time.Now().Add(time.Minute); runQuiet(backup) != nil; time.Sleep(2 * time.Second) {
		if time.Now().After(end) {
			s.sh(t, backup) // fails the test, with the output
			break
		}
	}
	if !waitFor(90*time.Second, func() bool {
		return s.query(t, `pagerarchive_backup_failing{job="pagerarchive"} == 0`) == 1 &&
			s.query(t, fmt.Sprintf(`pagerarchive_backup_last_good_timestamp_seconds{job="pagerarchive"} >= %d`, started)) == 1
	}) {
		t.Fatal("Prometheus never had this good backup: is Alloy scraping the API?")
	}
	t.Log("A80: a good backup, scraped: its time in the last-good metric, and not failing")

	// R86d: the backups dashboard shows it, each panel's query as Grafana runs it.
	one := func(title string) (float64, bool) {
		for _, v := range s.panelValues(t, "pa-backups", title)["A"] {
			return v, true
		}
		return 0, false
	}
	if v, ok := one("Since the last good backup"); !ok || v < 0 || v > 300 {
		t.Errorf("backups dashboard: since the last good backup %v s (shown %t), want under 5 minutes", v, ok)
	} else {
		t.Logf("R86d: since the last good backup: %.0f s", v)
	}
	if v, ok := one("Last backup"); !ok || v != 0 {
		t.Errorf("backups dashboard: last backup %v (shown %t), want 0, OK", v, ok)
	}
	for _, title := range []string{"Dump size, each night", "Dump time, each night", "Check time, each night"} {
		// A graph's points are a step apart, so a series seconds old may not
		// have one yet.
		waitFor(90*time.Second, func() bool { _, ok := one(title); return ok })
		if v, ok := one(title); !ok {
			t.Errorf("backups dashboard: %q shows nothing", title)
		} else {
			t.Logf("R86d: %s: %v", title, v)
		}
	}
	if v, _ := one("Dump size, each night"); v <= 0 {
		t.Errorf("backups dashboard: the dump size is %v", v)
	}

	since := time.Now()
	s.sh(t, os.Getenv("PA_MON_STOP_DB"))
	if err := runExpectingFailure(backup); err != nil {
		s.sh(t, os.Getenv("PA_MON_START_DB"))
		t.Fatal(err)
	}
	s.sh(t, os.Getenv("PA_MON_START_DB"))
	if !waitFor(2*time.Minute, func() bool {
		_, ok := hook.find(since, "Backup failing", "firing", "")
		return ok
	}) {
		t.Fatal(`"Backup failing" didn't fire within 2 minutes of a failed backup`)
	}
	at, _ := hook.find(since, "Backup failing", "firing", "")
	t.Logf("A80: \"Backup failing\" fired %s after the database stopped", at.Sub(since).Round(time.Second))
	if v, ok := one("Last backup"); !ok || v != 1 {
		t.Errorf("backups dashboard: last backup %v (shown %t) after a failure, want 1, FAILING", v, ok)
	} else {
		t.Log("R86d: the backups dashboard says FAILING")
	}

	since = time.Now()
	waitDB := time.Now().Add(time.Minute)
	for {
		if err := runQuiet(backup); err == nil {
			break
		}
		if time.Now().After(waitDB) {
			t.Fatal("no good backup within a minute of the database starting")
		}
		time.Sleep(2 * time.Second)
	}
	if !waitFor(3*time.Minute, func() bool {
		_, ok := hook.find(since, "Backup failing", "resolved", "")
		return ok
	}) {
		t.Fatal(`"Backup failing" didn't resolve after a good backup`)
	}
	t.Log("A80: it resolved after the next good backup")
}
