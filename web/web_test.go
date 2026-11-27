package web

import (
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

const page = `<!doctype html>
<html lang="en-AU">
	<head><link rel="stylesheet" href="/_app/immutable/app.css"></head>
	<body></body>
</html>`

// R82d (ADR 0028): the app's page goes out with the asker's look on its
// root element, so the first paint is already that look.
func TestPageIsSentWithTheLook(t *testing.T) {
	root := fstest.MapFS{"index.html": {Data: []byte(page)}, "_app/immutable/app.css": {Data: []byte("body{}")}}
	h := Serve(root, func(r *http.Request) (string, bool) {
		return r.Header.Get("X-Test-Look"), r.Header.Get("X-Test-Shared") != ""
	})
	get := func(path, look string) (*http.Response, string) {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("X-Test-Look", look)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		b, _ := io.ReadAll(w.Result().Body)
		return w.Result(), string(b)
	}
	for _, path := range []string{"/", "/index.html", "/albums/3", "/admin/users"} {
		res, body := get(path, "console")
		if !strings.Contains(body, `<html data-look="console" lang="en-AU">`) {
			t.Errorf("%s as console: %q", path, body)
		}
		if res.Header.Get("Vary") != "Cookie" || res.Header.Get("Cache-Control") != "no-cache, private" {
			t.Errorf("%s: Vary %q, Cache-Control %q", path, res.Header.Get("Vary"), res.Header.Get("Cache-Control"))
		}
	}
	// Before sign-in: the page as built, Harbour.
	if _, body := get("/", ""); body != page {
		t.Errorf("no look: %q", body)
	}
	// Anything but a plain name is never written into the page.
	for _, bad := range []string{`console" onload="x`, "Console", "con sole", "<b>", strings.Repeat("a", 33)} {
		if _, body := get("/", bad); body != page {
			t.Errorf("look %q: %q", bad, body)
		}
	}
	// R92b: a look that follows the shared look cookie says so.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Test-Look", "harbour")
	r.Header.Set("X-Test-Shared", "1")
	sw := httptest.NewRecorder()
	h.ServeHTTP(sw, r)
	if b, _ := io.ReadAll(sw.Result().Body); !strings.Contains(string(b), `<html data-look-shared="" data-look="harbour" lang="en-AU">`) {
		t.Errorf("shared: %q", b)
	}
	// Other files are served as they are.
	if _, body := get("/_app/immutable/app.css", "console"); body != "body{}" {
		t.Errorf("stylesheet: %q", body)
	}
	// No look function: the page as built.
	w := httptest.NewRecorder()
	Serve(root, nil).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if b, _ := io.ReadAll(w.Result().Body); string(b) != page {
		t.Errorf("nil look: %q", b)
	}
}

// R90 (ADR 0037): the installed app's files. The manifest goes out as a
// manifest; the service worker is checked for a new release on every
// navigation (no-cache, never immutable) and carries the page's policy,
// which limits what it may fetch to this origin; the offline page carries
// it too. Built files keep their year-long cache.
func TestInstalledAppFiles(t *testing.T) {
	root := fstest.MapFS{
		"index.html":            {Data: []byte(page)},
		"manifest.webmanifest":  {Data: []byte(`{"name":"PagerArchive"}`)},
		"service-worker.js":     {Data: []byte("self.addEventListener('fetch', () => {});")},
		"offline.html":          {Data: []byte("<!doctype html><title>You're offline</title>")},
		"_app/immutable/app.js": {Data: []byte("export {};")},
	}
	h := Serve(root, nil)
	for _, c := range []struct{ path, typ, cache, csp string }{
		{"/manifest.webmanifest", "application/manifest+json", "no-cache", ""},
		{"/service-worker.js", "text/javascript; charset=utf-8", "no-cache", selfOnly},
		{"/offline.html", "text/html; charset=utf-8", "no-cache", selfOnly},
		{"/_app/immutable/app.js", "text/javascript; charset=utf-8", "public, max-age=31536000, immutable", ""},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, c.path, nil))
		res := w.Result()
		if res.StatusCode != http.StatusOK {
			t.Errorf("%s: %d", c.path, res.StatusCode)
		}
		if got := res.Header.Get("Content-Type"); got != c.typ {
			t.Errorf("%s: Content-Type %q, want %q", c.path, got, c.typ)
		}
		if got := res.Header.Get("Cache-Control"); got != c.cache {
			t.Errorf("%s: Cache-Control %q, want %q", c.path, got, c.cache)
		}
		if got := res.Header.Get("Content-Security-Policy"); got != c.csp {
			t.Errorf("%s: Content-Security-Policy %q, want %q", c.path, got, c.csp)
		}
	}
}

// selfOnly is the policy of a page with no inline script, and of the service
// worker: R36a's, with scripts from this origin only.
const selfOnly = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'"

// A153 (R36a, ADR 0053): the app's page goes out with the SHA-256 of each of
// its own inline scripts in script-src, worked out from the page as built,
// and never 'unsafe-inline' or 'unsafe-eval' for scripts.
func TestPolicyHashesThePagesOwnInlineScripts(t *testing.T) {
	// The hashes are worked out here independently: base64 SHA-256 of each
	// script's text as the browser reads it (CRLF read as LF).
	first := "\n\t\t\tvar a = 1;\n\t\t"
	second := "console.log('second')"
	sum := func(s string) string {
		h := sha256.Sum256([]byte(s))
		return "'sha256-" + base64.StdEncoding.EncodeToString(h[:]) + "'"
	}
	built := `<!doctype html>
<html lang="en-AU">
	<head><script type="module" src="/_app/immutable/start.js"></script><link rel="stylesheet" href="/_app/immutable/app.css"></head>
	<body><div style="display: contents"><script>` + strings.ReplaceAll(first, "\n", "\r\n") + `</script><SCRIPT nonce="x">` + second + `</SCRIPT><script>` + second + `</script></div></body>
</html>`
	want := strings.Replace(selfOnly, "script-src 'self';", "script-src 'self' "+sum(first)+" "+sum(second)+";", 1)

	root := fstest.MapFS{"index.html": {Data: []byte(built)}}
	h := Serve(root, func(r *http.Request) (string, bool) { return "console", r.URL.Path == "/albums/3" })
	for _, path := range []string{"/", "/index.html", "/albums/3", "/signin"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		got := w.Result().Header.Get("Content-Security-Policy")
		if got != want {
			t.Errorf("%s: Content-Security-Policy\n %q\nwant\n %q", path, got, want)
		}
		scripts := got[strings.Index(got, "script-src"):]
		scripts = scripts[:strings.Index(scripts, ";")]
		if strings.Contains(scripts, "unsafe-inline") || strings.Contains(scripts, "unsafe-eval") {
			t.Errorf("%s: %s", path, scripts)
		}
	}
	// A page with no inline script: this origin's scripts only.
	if got := policy([]byte(page)); got != selfOnly {
		t.Errorf("no inline script: %q", got)
	}
}
