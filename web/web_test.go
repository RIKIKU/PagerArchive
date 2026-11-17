package web

import (
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
	h := Serve(root, func(r *http.Request) string { return r.Header.Get("X-Test-Look") })
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
		{"/service-worker.js", "text/javascript; charset=utf-8", "no-cache", csp},
		{"/offline.html", "text/html; charset=utf-8", "no-cache", csp},
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
