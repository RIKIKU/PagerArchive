package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// R40a / A101: the post-sign-in return path stays on this site.
func TestA101LocalPath(t *testing.T) {
	kept := []string{
		"/",
		"/jobs/EX0000001",
		"/search?q=SHED+FIRE",
		"/search?q=SHED%20FIRE&from=2026-01-01",
		"/aliases/12?tab=pages#latest",
		"/capcodes/0300001?from=2026-01-01T00:00:00%2B10:00",
		"/reports/capcode?capcode=0300001&alias=Station%201",
		"/signin",
		"/authors",
		"/a%2Fb",
	}
	for _, p := range kept {
		if got := localPath(p); got != p {
			t.Errorf("localPath(%q) = %q, want it kept", p, got)
		}
	}
	refused := []string{
		"",
		"jobs",
		"//evil.example",
		"///evil.example",
		"/\\evil.example",
		"\\\\evil.example",
		"/x\\y",
		"/\t/evil.example",
		"/\n/evil.example",
		"/\r/evil.example",
		"/\x00/evil.example",
		"/\x7f/evil.example",
		"/ /evil.example",
		"/x?q=a b",
		"/x?q=a\tb",
		"/x#a\nb",
		"/%09/evil.example",
		"/%0A/evil.example",
		"/%0D/evil.example",
		"/%00/evil.example",
		"/%20/evil.example",
		"/%5Cevil.example",
		"/%5c%5cevil.example",
		"/%2F%2Fevil.example",
		"/%2f/evil.example",
		"/%zz",
		"https://evil.example/",
		"http:evil.example",
		"javascript:alert(1)",
		"/。evil.example",          // ideographic full stop
		"/／／evil.example",         // fullwidth solidus
		"/ /evil.example",         // line separator
		"/%E2%80%A8/evil.example", // line separator, encoded
		"/%EF%BC%8Fevil.example",  // fullwidth solidus, encoded
		"/auth/login",
		"/auth/callback?code=x",
		"/auth",
		"/%61uth/login",
		"/./auth/login",
		"/x/../auth/login",
	}
	for _, p := range refused {
		if got := localPath(p); got != "/" {
			t.Errorf("localPath(%q) = %q, want \"/\"", p, got)
		}
	}
}

// What the browser is sent: a refused value redirects to "/", a kept one to
// itself, through the same http.Redirect the callback uses.
func TestA101LocalPathRedirect(t *testing.T) {
	for p, want := range map[string]string{
		"/\t/evil.example":      "/",
		"/%09/evil.example":     "/",
		"/\\evil.example":       "/",
		"/jobs/EX0000001?x=1#y": "/jobs/EX0000001?x=1#y",
		"/search?q=SHED%20FIRE": "/search?q=SHED%20FIRE",
	} {
		w := httptest.NewRecorder()
		http.Redirect(w, httptest.NewRequest("GET", "/auth/callback", nil), localPath(p), http.StatusSeeOther)
		if got := w.Header().Get("Location"); got != want {
			t.Errorf("return %q: Location %q, want %q", p, got, want)
		}
	}
}
