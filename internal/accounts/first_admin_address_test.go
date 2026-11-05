package accounts

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

type holdersDir struct {
	Directory
	holders EmailHolders
	fail    int // this many calls fail first
	asked   []string
}

func (d *holdersDir) EmailHolders(_ context.Context, address string) (EmailHolders, error) {
	d.asked = append(d.asked, address)
	if d.fail > 0 {
		d.fail--
		return EmailHolders{}, errors.New("find address: HTTP 403")
	}
	return d.holders, nil
}

// R44c: one warning when another account holds the address, with what to do,
// and the address never in the log.
func TestR44cFirstAdminAddressWarning(t *testing.T) {
	const address = "owner@example.org"
	for _, tt := range []struct {
		name    string
		holders EmailHolders
		fail    int
		warns   bool
	}{
		{"akadmin holds it, before enrolment", EmailHolders{Count: 1, Administrator: true}, 0, true},
		{"akadmin and the first admin hold it", EmailHolders{Count: 2, Administrator: true}, 0, true},
		{"two other accounts hold it", EmailHolders{Count: 2}, 0, true},
		{"only the first admin holds it", EmailHolders{Count: 1}, 0, false},
		{"nobody holds it yet", EmailHolders{}, 0, false},
		{"Authentik answers only after a while", EmailHolders{Count: 2, Administrator: true}, 3, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var log bytes.Buffer
			dir := &holdersDir{holders: tt.holders, fail: tt.fail}
			s := &Service{Dir: dir, InitialAdmin: address, Log: slog.New(slog.NewTextHandler(&log, nil))}
			s.CheckFirstAdminAddress(context.Background(), time.Millisecond, 5)
			got := strings.Count(log.String(), "level=WARN")
			if want := map[bool]int{true: 1}[tt.warns]; got != want {
				t.Errorf("warnings: %d, want %d; log: %s", got, want, log.String())
			}
			if tt.warns && !strings.Contains(log.String(), "docs/deploy.md") {
				t.Errorf("the warning doesn't say what to do: %s", log.String())
			}
			if strings.Contains(log.String(), address) {
				t.Errorf("the address is in the log: %s", log.String())
			}
			if dir.asked[0] != address {
				t.Errorf("asked about %q", dir.asked[0])
			}
		})
	}
}

// A failure to ask doesn't stop anything: it says so once, without the address.
func TestR44cFirstAdminAddressCantAsk(t *testing.T) {
	var log bytes.Buffer
	dir := &holdersDir{fail: 3}
	s := &Service{Dir: dir, InitialAdmin: "owner@example.org", Log: slog.New(slog.NewTextHandler(&log, nil))}
	s.CheckFirstAdminAddress(context.Background(), time.Millisecond, 3)
	if len(dir.asked) != 3 {
		t.Errorf("asked %d times, want 3", len(dir.asked))
	}
	if strings.Count(log.String(), "\n") != 1 || !strings.Contains(log.String(), "couldn't ask Authentik") {
		t.Errorf("log: %s", log.String())
	}
	if strings.Contains(log.String(), "owner@example.org") {
		t.Errorf("the address is in the log: %s", log.String())
	}
}

// Without INITIAL_ADMIN_EMAIL there's nothing to ask.
func TestR44cNoFirstAdminAddress(t *testing.T) {
	var log bytes.Buffer
	dir := &holdersDir{}
	s := &Service{Dir: dir, Log: slog.New(slog.NewTextHandler(&log, nil))}
	s.CheckFirstAdminAddress(context.Background(), time.Millisecond, 3)
	if len(dir.asked) != 0 || log.Len() != 0 {
		t.Errorf("asked %v; log: %s", dir.asked, log.String())
	}
}
