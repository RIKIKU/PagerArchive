// Package web serves the browser app, built by SvelteKit (adapter-static)
// into web/build and embedded in the server binary (ADR 0001).
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:build
var files embed.FS

// Handler serves the built app. Unknown paths get index.html so the
// browser's router can handle them. If the app wasn't built (e.g. a Go-only
// test build), it says so.
func Handler() http.Handler {
	root, _ := fs.Sub(files, "build")
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
					w.Header().Set("Cache-Control", "no-cache")
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
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", csp)
		_, _ = w.Write(index)
	})
}

// csp allows only this origin. Map tiles are self-hosted (R35), so no third
// party is ever contacted. SvelteKit's static bootstrap script is inline.
const csp = "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'"
