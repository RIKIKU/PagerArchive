package monitortest

import (
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

	backup := os.Getenv("PA_MON_BACKUP_NOW")
	s.sh(t, backup)
	if !waitFor(90*time.Second, func() bool {
		return s.query(t, `pagerarchive_backup_failing{job="pagerarchive"} == 0`) == 1 &&
			s.query(t, `pagerarchive_backup_last_good_timestamp_seconds{job="pagerarchive"}`) == 1
	}) {
		t.Fatal("Prometheus never had a good backup: is Alloy scraping the API?")
	}
	t.Log("A80: a good backup, scraped: its time in the last-good metric, and not failing")

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
