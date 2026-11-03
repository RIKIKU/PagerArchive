package pagerarchive

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// What a deployment runs (SPEC.md R84, R84a, ADR 0019). The stacks themselves
// are A77, A78 and monitoring-checks.sh; these say what's wrong in a second.

type composeFile struct {
	path     string // relative to the repository
	Services map[string]struct {
		Image   string            `yaml:"image"`
		Build   any               `yaml:"build"`
		Labels  map[string]string `yaml:"labels"`
		Volumes []string          `yaml:"volumes"`
	} `yaml:"services"`
	Volumes map[string]any `yaml:"volumes"`
}

func composeFiles(t *testing.T) []composeFile {
	t.Helper()
	var out []composeFile
	for _, path := range []string{"compose.yaml", "deploy/monitoring/compose.yaml"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		c := composeFile{path: path}
		if err := yaml.Unmarshal(data, &c); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		out = append(out, c)
	}
	return out
}

// The upstream images a deployment may pull. Adding one is a decision, not a
// side effect of editing a compose file.
var upstream = []string{
	"postgis/postgis", "ghcr.io/goauthentik/server",
	"grafana/alloy", "grafana/tempo", "grafana/loki", "prom/prometheus", "grafana/grafana",
}

var pinned = regexp.MustCompile(`^([^:@]+(?::\d+)?[^:@]*):[^:@]+@sha256:[0-9a-f]{64}$`)

const ourImage = "ghcr.io/rikiku/pagerarchive"

// R84: every image is PagerArchive's own, or a known upstream one pinned by
// version and digest, and nothing is built where it's deployed.
func TestR84ImagesAreOursOrPinnedUpstream(t *testing.T) {
	for _, c := range composeFiles(t) {
		for name, s := range c.Services {
			if s.Build != nil {
				t.Errorf("%s: %s has a build: a deployment has nothing to build from", c.path, name)
			}
			if strings.HasPrefix(s.Image, ourImage+":") {
				continue
			}
			m := pinned.FindStringSubmatch(s.Image)
			if m == nil {
				t.Errorf("%s: %s's image %q isn't pinned by version and digest", c.path, name, s.Image)
			} else if !slices.Contains(upstream, m[1]) {
				t.Errorf("%s: %s's image %q isn't ours or a known upstream one", c.path, name, s.Image)
			}
		}
	}
}

// R84a: compose.yaml names this release's image, so getting the file is the
// upgrade, and no setting in .env.example pins a version.
func TestR84aComposeNamesThisRelease(t *testing.T) {
	want := ourImage + ":" + Version
	found := 0
	for _, c := range composeFiles(t) {
		for name, s := range c.Services {
			if strings.HasPrefix(s.Image, ourImage+":") {
				found++
				if s.Image != want {
					t.Errorf("%s: %s's image is %s; set it to %s", c.path, name, s.Image, want)
				}
			}
		}
	}
	if found == 0 {
		t.Errorf("no service runs %s", want)
	}
	env, err := os.ReadFile(".env.example")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(env, []byte("PAGERARCHIVE_VERSION")) {
		t.Error(".env.example has a setting for the version")
	}
}

// mountSource is a mount's source if it's a file from the release's archive
// (relative to the repository), or "" for a named volume or the deployment's
// own data.
func mountSource(t *testing.T, c composeFile, service, volume string) string {
	t.Helper()
	if strings.HasPrefix(volume, "${PROFILES_DIR:-") || strings.HasPrefix(volume, "${MAP_TILES_FILE:-") {
		return ""
	}
	source, _, _ := strings.Cut(volume, ":")
	_, named := c.Volumes[source]
	switch {
	case named:
		return ""
	case strings.HasPrefix(source, "./"):
		return filepath.ToSlash(filepath.Join(filepath.Dir(c.path), source))
	}
	t.Errorf("%s: %s mounts %q, which isn't a named volume, the deployment's data or a file from the archive", c.path, service, volume)
	return ""
}

func releaseAssets(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	out, err := exec.Command("sh", "scripts/ci/release-assets.sh", dir).CombinedOutput()
	if err != nil {
		t.Fatalf("release-assets.sh: %v\n%s", err, out)
	}
	return dir
}

// R84: every file a stack mounts is in the archive, one file at a time (a file
// a later release drops can't stay in use), and the stacks need nothing else
// from the repository.
func TestR84MountsComeFromTheArchive(t *testing.T) {
	dir := releaseAssets(t)
	out, err := exec.Command("tar", "-tzf", filepath.Join(dir, "pagerarchive-config.tar.gz")).Output()
	if err != nil {
		t.Fatal(err)
	}
	inArchive := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, c := range composeFiles(t) {
		for name, s := range c.Services {
			for _, v := range s.Volumes {
				src := mountSource(t, c, name, v)
				if src == "" {
					continue
				}
				if !slices.Contains(inArchive, src) {
					t.Errorf("%s: %s mounts %s, which isn't in the archive", c.path, name, src)
				}
				if fi, err := os.Stat(src); err != nil || fi.IsDir() {
					t.Errorf("%s: %s mounts %s, which must be a file: %v", c.path, name, src, err)
				}
			}
		}
	}
	for _, f := range inArchive {
		if strings.HasSuffix(f, "compose.yaml") || strings.Contains(f, "/test/") || strings.HasSuffix(f, ".env") || strings.Contains(strings.ToUpper(f), "PRIVATE") {
			t.Errorf("the archive holds %s, which doesn't belong in it", f)
		}
	}
}

// R84a: Compose doesn't notice a mounted file changing, so each service's
// label holds a hash of its files, and a release's change to one recreates the
// service on `docker compose up -d`. The database's first-start script is left
// out, as it means nothing to a database that already exists.
func TestR84aChangedConfigRecreatesItsService(t *testing.T) {
	for _, c := range composeFiles(t) {
		for name, s := range c.Services {
			h := sha256.New()
			files := 0
			for _, v := range s.Volumes {
				src := mountSource(t, c, name, v)
				if src == "" || strings.Contains(v, "/docker-entrypoint-initdb.d/") {
					continue
				}
				data, err := os.ReadFile(src)
				if err != nil {
					t.Fatal(err)
				}
				sum := sha256.Sum256(data)
				h.Write([]byte(src + " " + hex.EncodeToString(sum[:]) + "\n"))
				files++
			}
			label := s.Labels["pagerarchive.config"]
			if files == 0 {
				if label != "" {
					t.Errorf("%s: %s has a config label but mounts no config", c.path, name)
				}
				continue
			}
			if want := hex.EncodeToString(h.Sum(nil))[:16]; label != want {
				t.Errorf("%s: %s's files changed, so the release wouldn't recreate it: set its pagerarchive.config label to \"%s\"", c.path, name, want)
			}
		}
	}
}

// R84: the release attaches every file, unchanged from the repository, and the
// job really uploads them.
func TestR84ReleaseAttachesTheFiles(t *testing.T) {
	dir := releaseAssets(t)
	list, err := exec.Command("sh", "scripts/ci/release-assets.sh", "--list").Output()
	if err != nil {
		t.Fatal(err)
	}
	names := strings.Fields(string(list))
	for name, from := range map[string]string{
		"compose.yaml": "compose.yaml", "env.example": ".env.example",
		"monitoring-compose.yaml": "deploy/monitoring/compose.yaml", "monitoring-env.example": "deploy/monitoring/.env.example",
	} {
		if !slices.Contains(names, name) {
			t.Errorf("--list doesn't name %s", name)
		}
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Errorf("the release's files have no %s: %v", name, err)
			continue
		}
		want, err := os.ReadFile(from)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s differs from %s", name, from)
		}
	}
	if !slices.Contains(names, "pagerarchive-config.tar.gz") {
		t.Error("--list doesn't name the archive")
	}
	if _, err := os.Stat(filepath.Join(dir, "pagerarchive-config.tar.gz")); err != nil {
		t.Errorf("the release's files have no archive: %v", err)
	}

	var ci struct {
		Jobs map[string]struct {
			Steps []struct {
				Run string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	data, err := os.ReadFile(".github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(data, &ci); err != nil {
		t.Fatal(err)
	}
	var run strings.Builder
	for _, s := range ci.Jobs["release"].Steps {
		run.WriteString(s.Run + "\n")
	}
	for _, want := range []string{"sh scripts/ci/release-assets.sh dist", `gh release upload "$TAG" dist/*`, "release-assets.sh --list"} {
		if !strings.Contains(run.String(), want) {
			t.Errorf("the release job doesn't run %q", want)
		}
	}
}

// R84b: filling in .env is enough: every setting a stack insists on is in its
// example, and the docs no longer send anyone to a checkout.
func TestR84bInstallIsTheFilesAndEnv(t *testing.T) {
	required := regexp.MustCompile(`\$\{([A-Z_]+):\?`)
	for compose, example := range map[string]string{"compose.yaml": ".env.example", "deploy/monitoring/compose.yaml": "deploy/monitoring/.env.example"} {
		c, err := os.ReadFile(compose)
		if err != nil {
			t.Fatal(err)
		}
		e, err := os.ReadFile(example)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range required.FindAllSubmatch(c, -1) {
			if !regexp.MustCompile(`(?m)^` + string(m[1]) + `=`).Match(e) {
				t.Errorf("%s needs %s, which isn't in %s", compose, m[1], example)
			}
		}
	}
	for _, doc := range []string{"docs/deploy.md", "docs/monitoring.md"} {
		data, err := os.ReadFile(doc)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte("git pull")) {
			t.Errorf("%s still says git pull", doc)
		}
	}
}
