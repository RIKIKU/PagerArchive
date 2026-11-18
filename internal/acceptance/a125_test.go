package acceptance

import (
	"net/http"
	"sync"
	"testing"

	"github.com/RIKIKU/PagerArchive/internal/server"
)

// A125 at the API level (R77b): the "New release" notice is shown once per
// user for each release installed, on every device, never for a build that
// isn't a release, and silently the first time. (The browser half is in
// web/tests/e2e.spec.ts.)
func TestA125ReleaseSeen(t *testing.T) {
	e := setup(t)
	ask := func(m *member) server.ReleaseSeen {
		t.Helper()
		var out server.ReleaseSeen
		if code, p := m.call(http.MethodPost, "/api/v1/me/release-seen", nil, &out); code != http.StatusOK {
			t.Fatalf("%s asks: %d %s", m.email, code, p.Error)
		}
		return out
	}
	recorded := func(m *member) string {
		t.Helper()
		var v *string
		if err := e.st.Pool.QueryRow(e.ctx, "SELECT seen_release FROM users WHERE id = $1", m.id).Scan(&v); err != nil {
			t.Fatal(err)
		}
		if v == nil {
			return "(none)"
		}
		return *v
	}
	// A second browser: another session of the same user.
	second, err := e.signIn(e.user.claims)
	if err != nil {
		t.Fatal(err)
	}

	// Nothing recorded: the first release is recorded silently.
	e.srv.Version = "1.9.0"
	if got := recorded(e.user); got != "(none)" {
		t.Fatalf("before any ask, %s is recorded", got)
	}
	if got := ask(e.user); got.Version != "1.9.0" || got.Show {
		t.Errorf("first ask on 1.9.0: %+v, want 1.9.0 not shown", got)
	}
	if got := recorded(e.user); got != "1.9.0" {
		t.Errorf("after the first ask, %s is recorded, want 1.9.0", got)
	}
	if got := ask(e.user); got.Show {
		t.Errorf("asking again on 1.9.0: %+v, want not shown", got)
	}

	// The next release: shown once, then not on a reload or a second browser.
	e.srv.Version = "1.10.0"
	adminBefore := recorded(e.admin)
	if got := ask(e.user); got.Version != "1.10.0" || !got.Show {
		t.Errorf("first ask on 1.10.0: %+v, want 1.10.0 shown", got)
	}
	if got := ask(e.user); got.Show {
		t.Errorf("a reload on 1.10.0: %+v, want not shown", got)
	}
	if got := ask(second); got.Show {
		t.Errorf("a second browser on 1.10.0: %+v, want not shown", got)
	}
	if got := recorded(e.admin); got != adminBefore {
		t.Errorf("the user's asks changed the admin's record: %s, was %s", got, adminBefore)
	}
	// The other user's own: theirs, and silent the first time.
	if got := ask(e.admin); got.Show {
		t.Errorf("the admin's first ask on 1.10.0: %+v, want not shown", got)
	}
	if got := recorded(e.user); got != "1.10.0" {
		t.Errorf("the admin's ask changed the user's record: %s", got)
	}

	// Two at the same moment: exactly one is shown.
	for _, v := range []string{"1.10.1", "1.10.2", "1.10.3", "1.10.4", "1.10.5"} {
		e.srv.Version = v
		var wg sync.WaitGroup
		got := make([]server.ReleaseSeen, 2)
		for i, m := range []*member{e.user, second} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				var out server.ReleaseSeen
				if code, p := m.call(http.MethodPost, "/api/v1/me/release-seen", nil, &out); code != http.StatusOK {
					t.Errorf("%s at once: %d %s", v, code, p.Error)
				}
				got[i] = out
			}()
		}
		wg.Wait()
		if got[0].Show == got[1].Show {
			t.Errorf("two asks at once on %s: %+v and %+v, want exactly one shown", v, got[0], got[1])
		}
	}

	// A build that isn't a release: never shown, nothing recorded.
	for _, v := range []string{"1.11.0-dev+abc1234", "0.0.0-dev"} {
		e.srv.Version = v
		if got := ask(e.user); got.Version != v || got.Show {
			t.Errorf("%s: %+v, want not shown", v, got)
		}
		if got := recorded(e.user); got != "1.10.5" {
			t.Errorf("%s recorded %s, want still 1.10.5", v, got)
		}
	}
	e.srv.Version = "1.10.5"
	if got := ask(e.user); got.Show {
		t.Errorf("back on 1.10.5 after a dev build: %+v, want not shown", got)
	}

	// Only ever about the user asking; signed out, nothing.
	e.srv.Version = "1.11.0"
	if code, _ := e.user.call(http.MethodPost, "/api/v1/me/release-seen", map[string]any{"user_id": e.admin.id}, nil); code != http.StatusBadRequest {
		t.Errorf("a body naming another user: %d, want 400", code)
	}
	if got := recorded(e.admin); got != "1.10.0" {
		t.Errorf("after the refused ask, the admin's record is %s, want 1.10.0", got)
	}
	if code, _ := (&member{e: e}).call(http.MethodPost, "/api/v1/me/release-seen", nil, nil); code != http.StatusUnauthorized {
		t.Errorf("signed out: %d, want 401", code)
	}
	if got := ask(e.user); !got.Show {
		t.Errorf("1.11.0 after the refused asks: %+v, want shown", got)
	}
}
