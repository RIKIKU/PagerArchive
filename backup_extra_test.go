package pagerarchive

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// R86e, A135: BACKUP_EXTRA_DATABASES, as the backup script reads it
// (`pagerarchive-backup databases` lists what a night backs up, or says why
// the setting is refused, as every backup then does). The backups
// themselves are scripts/backup-checks.sh's and A80's.
func TestA135BackupExtraDatabasesSetting(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("needs sh")
	}
	own := "pagerarchive (PostgreSQL's postgres)\nauthentik\n"
	good := map[string]string{
		"":                                    own,
		"companion":                           own + "companion\n",
		"companion:calls,transmissions":       own + "companion (rows left out: public.calls,public.transmissions)\n",
		"companion:calls,transmissions;other": own + "companion (rows left out: public.calls,public.transmissions)\nother\n",
		"a_1:s.t;b;c:u":                       own + "a_1 (rows left out: s.t)\nb\nc (rows left out: public.u)\n",
		"_x:" + strings.Repeat("t", 63):       own + "_x (rows left out: public." + strings.Repeat("t", 63) + ")\n",
	}
	for v, want := range good {
		out, err := backupDatabases(v)
		if err != nil || out != want {
			t.Errorf("%q: %v\n%s\nwant\n%s", v, err, out, want)
		}
	}
	bad := map[string]string{
		"companion calls":                      "has a space or line break",
		"companion\nother":                     "has a space or line break",
		"companion;":                           "has an empty entry",
		";companion":                           "has an empty entry",
		"a;;b":                                 "has an empty entry",
		"companion:":                           "companion has a colon but no tables",
		"companion:calls:x":                    "has more than one colon",
		"Companion":                            "Companion isn't a database name",
		"companion-app":                        "companion-app isn't a database name",
		"1st":                                  "1st isn't a database name",
		strings.Repeat("d", 64):                "isn't a database name",
		"postgres":                             "postgres isn't an extra database",
		"pagerarchive":                         "pagerarchive isn't an extra database",
		"authentik":                            "authentik isn't an extra database",
		"template1":                            "template1 isn't an extra database",
		"companion;companion":                  "names companion twice",
		"companion:calls,,x":                   "companion has an empty table name",
		"companion:calls,":                     "companion has an empty table name",
		"companion:Calls":                      "Calls in companion isn't a table name",
		"companion:a.b.c":                      "a.b.c in companion isn't a table name",
		"companion:call*":                      "call in companion isn't a table name",
		`companion:"calls"`:                    "calls in companion isn't a table name",
		"companion:calls;$(id)":                "id isn't a database name",
		"companion:calls';drop":                "calls in companion isn't a table name",
		"companion:" + strings.Repeat("t", 64): "in companion isn't a table name",
	}
	for v, want := range bad {
		out, err := backupDatabases(v)
		if err == nil {
			t.Errorf("%q was taken: %s", v, out)
			continue
		}
		if !strings.Contains(out, want) || strings.Count(strings.TrimSpace(out), "\n") != 0 {
			t.Errorf("%q: %q, want one line saying %q", v, out, want)
		}
		// The message goes into the status file's JSON and the log: no quote,
		// backslash or comma (the script's reader stops at one) from the value.
		if strings.ContainsAny(out, "\"\\,$*;") {
			t.Errorf("%q: the message carries the value's characters: %q", v, out)
		}
	}
}

func backupDatabases(v string) (string, error) {
	cmd := exec.Command("sh", "deploy/backup/backup.sh", "databases")
	cmd.Env = append(os.Environ(), "BACKUP_EXTRA_DATABASES="+v)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
