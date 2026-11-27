package web

import (
	"net/http"
	"regexp"
	"strings"
)

// ThemeCookie is the name of the colour theme's cookie (R92b): on
// LOOK_COOKIE_DOMAIN beside RadioArchive, host-only standalone. The page's
// script writes it (ArchiveUx's applyTheme, look.js); the server only reads
// it, for the first paint.
const ThemeCookie = "theme"

// ThemeFromCookies is the theme in a request's Cookie headers, "light" or
// "dark", or "" (the system's) when no cookie of that name holds one. It's
// ArchiveUx's themeFromCookies (look.js, 0.7.0): the value as it is (double
// quotes around it are dropped), and an empty or unknown one is passed over,
// as a browser that still holds a host-only cookie of the same name beside
// the shared one sends both.
func ThemeFromCookies(r *http.Request) string {
	for _, line := range r.Header.Values("Cookie") {
		for _, part := range strings.Split(line, ";") {
			name, v, ok := strings.Cut(part, "=")
			if !ok || strings.TrimSpace(name) != ThemeCookie {
				continue
			}
			v = strings.TrimSpace(v)
			if len(v) >= 2 && strings.HasPrefix(v, `"`) && strings.HasSuffix(v, `"`) {
				v = v[1 : len(v)-1]
			}
			if v == "light" || v == "dark" {
				return v
			}
		}
	}
	return ""
}

// WithTheme is the page with data-theme="theme" on its root element, for
// light or dark; anything else (the system's) leaves the page as it is.
func WithTheme(page []byte, theme string) []byte {
	if theme != "light" && theme != "dark" {
		return page
	}
	return withAttr(page, ` data-theme="`+theme+`"`)
}

var domainName = regexp.MustCompile(`^[a-z0-9.-]{1,253}$`)

// withCookieDomain is the page with the shared cookies' parent domain on its
// root element (data-cookie-domain), for the page's script to write the
// theme cookie on (R92b). "" or anything but a plain domain name leaves the
// page as it is.
func withCookieDomain(page []byte, domain string) []byte {
	if !domainName.MatchString(domain) {
		return page
	}
	return withAttr(page, ` data-cookie-domain="`+domain+`"`)
}
