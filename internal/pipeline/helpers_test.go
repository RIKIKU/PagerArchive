package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/RIKIKU/PagerArchive/internal/profile"
)

func loadAgencyJSON(t *testing.T, doc string) (*profile.Set, error) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.json"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return profile.LoadDir(dir)
}
