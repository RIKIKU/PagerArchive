// Command test-releases stands in for GitHub in A90 (scripts/a90.sh), never in
// a deployment: the releases API, the attestations API and the releases' files,
// all from a folder the test changes between its steps.
//
//	<dir>/releases.json   what GET /repos/<owner>/<repo>/releases returns
//	<dir>/attested        one "sha256:<hex>" per line: the digests with an attestation
//	<dir>/files/...       served at /files/...
//
// An attested digest's bundle is {"test": "sha256:<hex>"}, which only the
// stand-in GitHub CLI next to this (gh) accepts.
//
//	go run ./scripts/test-releases -dir DIR -addr 127.0.0.1:15080
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	dir := flag.String("dir", ".", "the folder")
	addr := flag.String("addr", "127.0.0.1:15080", "where to listen")
	flag.Parse()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/{owner}/{repo}/releases", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		http.ServeFile(w, r, filepath.Join(*dir, "releases.json"))
	})
	mux.HandleFunc("GET /repos/{owner}/{repo}/attestations/{digest}", func(w http.ResponseWriter, r *http.Request) {
		digest := r.PathValue("digest")
		f, err := os.Open(filepath.Join(*dir, "attested"))
		if err == nil {
			defer f.Close()
			for s := bufio.NewScanner(f); s.Scan(); {
				if strings.TrimSpace(s.Text()) == digest {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"attestations": []any{map[string]any{"bundle": map[string]string{"test": digest}}}})
					return
				}
			}
		}
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	})
	mux.Handle("GET /files/", http.StripPrefix("/files/", http.FileServer(http.Dir(filepath.Join(*dir, "files")))))
	log.Printf("test-releases: %s on %s", *dir, *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
