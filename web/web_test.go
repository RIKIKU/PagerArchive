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
