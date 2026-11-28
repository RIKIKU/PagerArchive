package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// A162 (R92b): the theme cookie is read as ArchiveUx's themeFromCookies
// (look.js, 0.7.0) reads it; its cases, in test/look.test.mjs.
func TestA162ThemeFromCookies(t *testing.T) {
	for _, c := range []struct{ cookies, want string }{
		{"", ""},
		{"theme=dark", "dark"},
		{"theme=light", "light"},
		{`theme="dark"`, "dark"},
		{"a=1; theme=light; b=2", "light"},
		{" theme = dark ", "dark"},
		{"theme=", ""},
		{"theme=system", ""},
		{"theme=Dark", ""},
		{"theme=blue", ""},
		{"mytheme=dark", ""},
		{"theme-x=dark", ""},
		{"look=console", ""},
		// An empty or unknown one is passed over for a later one: a browser
		// with a host-only cookie beside the shared one sends both.
		{"theme=; theme=dark", "dark"},
		{"theme=blue; theme=light", "light"},
		// Of two that hold a theme, the first counts.
		{"theme=light; theme=dark", "light"},
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if c.cookies != "" {
			r.Header.Set("Cookie", c.cookies)
		}
		if got := ThemeFromCookies(r); got != c.want {
			t.Errorf("%q: %q, want %q", c.cookies, got, c.want)
		}
	}
	// Two Cookie header lines (HTTP/2 may split them): read in order.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Add("Cookie", "theme=")
	r.Header.Add("Cookie", "theme=dark")
	if got := ThemeFromCookies(r); got != "dark" {
		t.Errorf("two lines: %q", got)
	}
}

// A162 (R92b, R36a): the page goes out in the cookie's theme, with the parent
// domain only when there is one, and the same policy whatever the cookies.
func TestA162PageIsSentInTheCookiesTheme(t *testing.T) {
	built := `<!doctype html>
<html lang="en-AU">
	<head></head>
	<body><script>start()</script></body>
</html>`
	root := fstest.MapFS{"index.html": {Data: []byte(built)}}
	get := func(h http.Handler, path, cookie string) (string, string) {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if cookie != "" {
			r.Header.Set("Cookie", cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		b, _ := io.ReadAll(w.Result().Body)
		return string(b), w.Result().Header.Get("Content-Security-Policy")
	}
	want := policy([]byte(built))

	alone := Serve(root, func(*http.Request) (string, bool) { return "harbour", false }, "")
	for _, c := range []struct{ cookie, root string }{
		{"", `<html data-look="harbour" lang="en-AU">`},
		{"theme=dark", `<html data-look="harbour" data-theme="dark" lang="en-AU">`},
		{"theme=light", `<html data-look="harbour" data-theme="light" lang="en-AU">`},
		{"theme=system", `<html data-look="harbour" lang="en-AU">`},
		{`theme=dark" onload="x`, `<html data-look="harbour" lang="en-AU">`},
	} {
		for _, path := range []string{"/", "/signin", "/albums/3"} {
			body, csp := get(alone, path, c.cookie)
			if !strings.Contains(body, c.root) {
				t.Errorf("standalone %s %q: %q", path, c.cookie, body)
			}
			if csp != want {
				t.Errorf("standalone %s %q: policy %q, want %q", path, c.cookie, csp, want)
			}
		}
	}

	shared := Serve(root, func(*http.Request) (string, bool) { return "console", true }, "example.org")
	body, csp := get(shared, "/", "theme=dark; look=console")
	if !strings.Contains(body, `<html data-look-shared="" data-look="console" data-theme="dark" data-cookie-domain="example.org" lang="en-AU">`) {
		t.Errorf("shared, dark: %q", body)
	}
	if csp != want {
		t.Errorf("shared: policy %q, want %q", csp, want)
	}
	if body, _ := get(shared, "/", ""); !strings.Contains(body, `<html data-look-shared="" data-look="console" data-cookie-domain="example.org" lang="en-AU">`) {
		t.Errorf("shared, system: %q", body)
	}
	// No look function: the theme all the same.
	if body, _ := get(Serve(root, nil, ""), "/", "theme=dark"); !strings.Contains(body, `<html data-theme="dark" lang="en-AU">`) {
		t.Errorf("nil look: %q", body)
	}
	// Anything but a plain domain name is never written.
	for _, bad := range []string{`example.org" onload="x`, "Example.org", "a b", ""} {
		if body, _ := get(Serve(root, nil, bad), "/", ""); strings.Contains(body, "data-cookie-domain") {
			t.Errorf("domain %q: %q", bad, body)
		}
	}
}
