// Package web serves the browser app, built by SvelteKit (adapter-static)
// into web/build and embedded in the server binary (ADR 0001).
package web

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
)

//go:embed all:build
var files embed.FS

// Handler serves the built app. Unknown paths get index.html so the
// browser's router can handle them. If the app wasn't built (e.g. a Go-only
// test build), it says so. look, if not nil, names the look to send the
// page with (R82d): see Serve.
func Handler(look LookFunc) http.Handler {
	root, _ := fs.Sub(files, "build")
	return Serve(root, look)
}

// Serve serves the app built into root. index.html, the app's one page, is
// sent with the request's look, look(r), as data-look on its root element,
// so the stylesheet draws the first paint in that look and the page never
// shows the default look first (R82d, ADR 0028). With no look, or "", the
// page is sent as built: the default look, Harbour. With shared, the page also
// says the look follows the look cookie the apps share (R92b), as
// data-look-shared, so the app's script follows it too.
func Serve(root fs.FS, look LookFunc) http.Handler {
	index, err := fs.ReadFile(root, "index.html")
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "The browser app wasn't built into this binary (cd web && npm run build).", http.StatusServiceUnavailable)
		})
	}
	fileServer := http.FileServerFS(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p != "" && p != "index.html" {
			if st, err := fs.Stat(root, p); err == nil && !st.IsDir() {
				if strings.HasPrefix(p, "_app/immutable/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				} else {
					// The service worker among them: a browser checks it for
					// a new release on every navigation (R90).
					w.Header().Set("Cache-Control", "no-cache")
				}
				switch {
				case strings.HasSuffix(p, ".webmanifest"):
					w.Header().Set("Content-Type", "application/manifest+json")
				case strings.HasSuffix(p, ".html"), p == "service-worker.js":
					// The offline page, and the service worker, whose own
					// policy limits what it may fetch: this origin only.
					w.Header().Set("Content-Security-Policy", csp)
				}
				fileServer.ServeHTTP(w, r)
				return
			}
			// Only Latin glyph ranges are bundled for the map (English only, R35a).
			// Any other range is an empty glyph set, not the app page.
			if strings.HasPrefix(p, "map/fonts/") && strings.HasSuffix(p, ".pbf") {
				w.Header().Set("Content-Type", "application/x-protobuf")
				w.Header().Set("Cache-Control", "public, max-age=86400")
				return
			}
		}
		page := index
		if look != nil {
			name, shared := look(r)
			page = WithLook(index, name)
			if shared {
				page = withAttr(page, ` data-look-shared=""`)
			}
		}
		// The page depends on who asks, so no cache may keep one person's.
		w.Header().Set("Cache-Control", "no-cache, private")
		w.Header().Set("Vary", "Cookie")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", csp)
		_, _ = w.Write(page)
	})
}

// LookFunc names the look a request's page is sent in, and whether it follows
// the look cookie the apps share (R92b).
type LookFunc func(*http.Request) (look string, shared bool)

var lookName = regexp.MustCompile(`^[a-z]{1,32}$`)

// WithLook is the page with data-look="look" on its root element. A look
// that isn't a plain lower-case name (or "") leaves the page as it is, so
// nothing but a name can ever be written into it.
func WithLook(page []byte, look string) []byte {
	if !lookName.MatchString(look) {
		return page
	}
	return withAttr(page, ` data-look="`+look+`"`)
}

// withAttr is the page with attr written into its root element.
func withAttr(page []byte, attr string) []byte {
	i := bytes.Index(page, []byte("<html"))
	if i < 0 {
		return page
	}
	i += len("<html")
	out := make([]byte, 0, len(page)+len(attr))
	out = append(out, page[:i]...)
	out = append(out, attr...)
	return append(out, page[i:]...)
}

// csp allows only this origin. Map tiles are self-hosted (R35), so no third
// party is ever contacted. SvelteKit's static bootstrap script is inline.
const csp = "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'"
