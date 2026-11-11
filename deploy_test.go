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
		Network string            `yaml:"network_mode"`
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

// These publish no major.minor tag or anything else that moves only within a
// minor version, so they name an exact version (R84; listed in ADR 0019).
var noMinorTag = []string{"grafana/alloy", "grafana/tempo", "prom/prometheus"}

const ourImage = "ghcr.io/rikiku/pagerarchive"

var (
	minorTag = regexp.MustCompile(`^v?\d+(\.\d+|-\d+\.\d+)$`)
	exactTag = regexp.MustCompile(`^v?\d+\.\d+\.\d+$`)
)

// R84: every image is PagerArchive's own or a known upstream one, and nothing
// is built where it's deployed.
func TestR84ImagesAreOursOrKnownUpstream(t *testing.T) {
	for _, c := range composeFiles(t) {
		for name, s := range c.Services {
			if s.Build != nil {
				t.Errorf("%s: %s has a build: a deployment has nothing to build from", c.path, name)
			}
			image, _, _ := strings.Cut(s.Image, "@")
			repo, _, _ := strings.Cut(image, ":")
			if repo != ourImage && !slices.Contains(upstream, repo) {
				t.Errorf("%s: %s's image %q isn't ours or a known upstream one", c.path, name, s.Image)
			}
		}
	}
}

// R84, A79: every image is named by a major.minor tag, so `docker compose pull`
// gets patches: no patch number and no digest, and nothing that moves further
// than a minor version. Only images with no such tag may name an exact version.
func TestA79ImagesAreNamedByMajorMinor(t *testing.T) {
	for _, c := range composeFiles(t) {
		for name, s := range c.Services {
			if strings.Contains(s.Image, "@") {
				t.Errorf("%s: %s's image %q has a digest: name it by its major.minor tag", c.path, name, s.Image)
				continue
			}
			repo, tag, _ := strings.Cut(s.Image, ":")
			switch {
			case repo == ourImage && Version == unstampedVersion:
				// The repository's own compose.yaml: a build writes its
				// MAJOR.MINOR there (ADR 0024).
				if tag != "main" {
					t.Errorf("%s: %s's image %q: the repository's own compose.yaml names it :main", c.path, name, s.Image)
				}
			case slices.Contains(noMinorTag, repo):
				if !exactTag.MatchString(tag) {
					t.Errorf("%s: %s's image %q: %s publishes no major.minor tag, so name an exact version", c.path, name, s.Image, repo)
				}
			case !minorTag.MatchString(tag):
				t.Errorf("%s: %s's image %q isn't named by a major.minor tag: no patch number, no latest", c.path, name, s.Image)
			}
		}
	}
}

// R84: compose.yaml names this release's minor version of our image, so its
// patch releases need no new file, and no setting in .env.example pins a version.
// The repository's own compose.yaml names :main, and a build writes its
// version's MAJOR.MINOR there (scripts/ci/stamp-version.sh, ADR 0024).
func TestR84aComposeNamesThisRelease(t *testing.T) {
	want := ourImage + ":main"
	if Version != unstampedVersion {
		v, _, _ := strings.Cut(Version, "-")
		want = ourImage + ":" + v[:strings.LastIndex(v, ".")]
	}
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
	if strings.HasPrefix(volume, "${PROFILES_DIR:-") || strings.HasPrefix(volume, "${MAP_TILES_FILE:-") || strings.HasPrefix(volume, "${BACKUP_DIR:-") {
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

	type job struct {
		Needs needs `yaml:"needs"`
		Steps []struct {
			Run string `yaml:"run"`
		} `yaml:"steps"`
	}
	var ci struct {
		Jobs map[string]job `yaml:"jobs"`
	}
	data, err := os.ReadFile(".github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(data, &ci); err != nil {
		t.Fatal(err)
	}
	runs := func(j job) string {
		var run strings.Builder
		for _, s := range j.Steps {
			run.WriteString(s.Run + "\n")
		}
		return run.String()
	}
	attach := ci.Jobs["attach-release-files"]
	for _, want := range []string{"sh scripts/ci/release-assets.sh dist", `gh release upload "$TAG" dist/*`, "release-assets.sh --list", `gh release edit "$TAG" --latest`} {
		if !strings.Contains(runs(attach), want) {
			t.Errorf("attach-release-files doesn't run %q", want)
		}
	}
	// Only once the images are there, and the release isn't the latest before then.
	for _, need := range []string{"release", "publish-server", "publish-client-manifest"} {
		if !slices.Contains(attach.Needs, need) {
			t.Errorf("attach-release-files doesn't wait for %s: an attached compose.yaml could name an image that isn't on GHCR yet", need)
		}
	}
	release := runs(ci.Jobs["release"])
	if strings.Contains(release, "release-assets.sh") {
		t.Error("the release job attaches the files, before the images are published")
	}
	if !strings.Contains(release, "--latest=false") {
		t.Error("the release is the latest before its files are attached: the docs' download links would find none")
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

// R44c: the example .env mustn't invite one address for the first admin and
// Authentik's administrator, and says why.
func TestR44cExampleAddressesDiffer(t *testing.T) {
	data, err := os.ReadFile(".env.example")
	if err != nil {
		t.Fatal(err)
	}
	value := func(key string) string {
		m := regexp.MustCompile(`(?m)^` + key + `=(.*)$`).FindSubmatch(data)
		if m == nil || len(m[1]) == 0 {
			t.Fatalf(".env.example has no example %s", key)
		}
		return strings.TrimSpace(string(m[1]))
	}
	first, admin := value("INITIAL_ADMIN_EMAIL"), value("AUTHENTIK_ADMIN_EMAIL")
	if strings.EqualFold(first, admin) {
		t.Errorf(".env.example gives INITIAL_ADMIN_EMAIL and AUTHENTIK_ADMIN_EMAIL the same address, %s: they must differ (R44c)", first)
	}
	if !bytes.Contains(bytes.ToLower(data), []byte("must differ")) {
		t.Error(".env.example doesn't say the two addresses must differ")
	}
}

// R86a, R86: the check has no network and no secrets, and only it and the backup
// service see the folder; the API sees only the status volume, read-only, so a
// compromised API can't read Authentik's password hashes from a dump.
func TestR86aBackupServices(t *testing.T) {
	c := composeFiles(t)[0]
	check, ok := c.Services["backup-check"]
	if !ok {
		t.Fatal("no backup-check service")
	}
	if check.Network != "none" {
		t.Errorf("backup-check's network_mode is %q, want none", check.Network)
	}
	data, err := os.ReadFile("compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Services map[string]struct {
			Environment map[string]string `yaml:"environment"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(data, &env); err != nil {
		t.Fatal(err)
	}
	for k := range env.Services["backup-check"].Environment {
		if strings.Contains(k, "PASSWORD") || strings.HasPrefix(k, "PG") {
			t.Errorf("backup-check has %s: it needs no way into the database", k)
		}
	}
	for name, s := range c.Services {
		for _, v := range s.Volumes {
			if strings.HasPrefix(v, "${BACKUP_DIR:-") && name != "backup" && name != "backup-check" {
				t.Errorf("%s mounts the backup folder", name)
			}
			if strings.HasPrefix(v, "backup-status:") && name == "api" && !strings.HasSuffix(v, ":ro") {
				t.Errorf("the API can write the backup status: %s", v)
			}
		}
	}
}

// R76: the monitoring stack owns its network, under that name, and PagerArchive
// doesn't appear in its compose file: the override joins them.
func TestR76MonitoringOwnsItsNetwork(t *testing.T) {
	data, err := os.ReadFile("deploy/monitoring/compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Networks map[string]struct {
			Name     string `yaml:"name"`
			External bool   `yaml:"external"`
		} `yaml:"networks"`
	}
	if err := yaml.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	if n, ok := c.Networks["monitoring"]; !ok || n.Name != "monitoring" || n.External {
		t.Errorf("networks.monitoring is %+v, want name: monitoring, not external", n)
	}
	for key, n := range c.Networks {
		if n.External {
			t.Errorf("network %s is external: this stack joins nobody's network", key)
		}
	}
	if bytes.Contains(data, []byte("PAGERARCHIVE_NETWORK")) {
		t.Error("the stack's compose file still names PagerArchive's network")
	}
}

// needs is a workflow job's needs: one job's name, or a list of them.
type needs []string

func (n *needs) UnmarshalYAML(v *yaml.Node) error {
	if v.Kind == yaml.ScalarNode {
		*n = needs{v.Value}
		return nil
	}
	var list []string
	err := v.Decode(&list)
	*n = list
	return err
}
