package stacktest

// A56 and A58 through the PowerShell module against the real stack (ADR
// 0011): templates for admins, and icons from a file and from a URL, stored
// as 64×64 PNGs. A57 runs in the browser on the full stack
// (web/tests/stack.spec.ts). Like the other module checks, these run once
// per vault setting on the same stack, so names are unique to each run.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/icon"
)

type adminTemplate struct {
	ID      int64   `json:"id"`
	Name    string  `json:"name"`
	Agency  string  `json:"agency"`
	Icon    *string `json:"icon"`
	Aliases int64   `json:"aliases"`
}

func (b *browser) template(name string) adminTemplate {
	b.t.Helper()
	var ts []adminTemplate
	if code, msg := b.api(http.MethodGet, "/api/v1/admin/templates", nil, &ts); code != http.StatusOK {
		b.t.Fatalf("templates: %d %s", code, msg)
	}
	for _, t := range ts {
		if t.Name == name {
			return t
		}
	}
	b.t.Fatalf("no template %q", name)
	return adminTemplate{}
}

// get fetches path, which may have a query, with b's session.
func (b *browser) get(path string) (*http.Response, []byte) {
	b.t.Helper()
	resp, err := b.client.Get(b.s.app.String() + path)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, body
}

// newestAgencyAndIcon is the agency and icon URL the message list shows for
// capcode's newest page.
func (b *browser) newestAgencyAndIcon(capcode string) (string, string) {
	b.t.Helper()
	var page struct {
		Messages []struct {
			Agency *string `json:"agency"`
			Icon   *string `json:"icon"`
		} `json:"messages"`
	}
	resp, body := b.get("/api/v1/messages?capcode=" + capcode + "&limit=1")
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &page) != nil || len(page.Messages) == 0 {
		b.t.Fatalf("messages for %s: %d", capcode, resp.StatusCode)
	}
	return deref(page.Messages[0].Agency), deref(page.Messages[0].Icon)
}

func (ps *psStack) postPage(t *testing.T, capcode, text string) {
	t.Helper()
	body := fmt.Sprintf(`{"capcode":%q,"text":%q,"received_at":%q}`, capcode, text, time.Now().UTC().Format(time.RFC3339Nano))
	req, _ := http.NewRequest(http.MethodPost, ps.app.JoinPath("/api/v1/pages").String(), strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+os.Getenv("PA_STACK_CLIENT_KEY"))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("post page: %d", resp.StatusCode)
	}
}

// stations makes sure Stations 1-4 exist on "Example Fire", through the module.
func stations(t *testing.T, w *psWindow) {
	t.Helper()
	exampleFire(t, w)
	if out := w.do(`foreach ($n in 1..4) {
    if (-not (Get-PagerArchiveAlias -Pattern "010000$n")) { $null = New-PagerArchiveAlias -Pattern "010000$n" -Name "Station $n" -Template 'Example Fire' }
}`); len(out) != 0 {
		t.Fatalf("stations: %q", out)
	}
}

func testIconPath(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("../../testdata/icons", name))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// A56. An admin adds a template, moves an alias to it, can't delete one in
// use; a user can't change templates through the API.
func TestPowerShellA56Templates(t *testing.T) {
	ps := setupPS(t)
	owner := ps.owner(t)
	w := ps.window(t, ps.psHome(t))
	ps.connect(w, owner, "")
	stations(t, w)
	run := ps.prefix + ps.run
	name := "Example Rescue " + run
	ps.postPage(t, "0100004", "@@EX"+run+"56 HSF1 SMOKE FROM SHED @ 3 FICTION ST, MADEUPTON #STN4")

	type template struct {
		Name       string
		Agency     string
		AliasCount int
		HasIcon    bool
	}
	got := jsonLines[template](t, w.do(`New-PagerArchiveTemplate -Name '`+name+`' -Agency 'Example Rescue Service' | Select-Object -Property Name, Agency, AliasCount, HasIcon`))
	if len(got) != 1 || got[0] != (template{name, "Example Rescue Service", 0, false}) {
		t.Fatalf("added: %+v", got)
	}
	t.Logf("A56: %q added with 0 aliases", name)

	// Station 4 moves to it: its pages show the new agency at once.
	renamed := name + " North"
	t.Cleanup(func() {
		w.do(`Set-PagerArchiveAlias -Name 'Station 4' -Template 'Example Fire'; Remove-PagerArchiveTemplate -Name '` + name + `', '` + renamed + `' -Confirm:$false`)
	})
	if out := w.do(`Set-PagerArchiveAlias -Name 'Station 4' -Template '` + name + `'`); len(out) != 0 {
		t.Fatalf("move Station 4: %q", out)
	}
	if agency, _ := owner.newestAgencyAndIcon("0100004"); agency != "Example Rescue Service" {
		t.Errorf("0100004's page shows agency %q", agency)
	}
	if n := owner.template(name).Aliases; n != 1 {
		t.Errorf("%s has %d aliases", name, n)
	}

	// Renaming (R63): Station 4 keeps the template under its new name; a
	// name another template has is refused; renaming back works.
	type renamedTemplate struct {
		Name       string
		AliasCount int
	}
	if got := jsonLines[renamedTemplate](t, w.do(`Rename-PagerArchiveTemplate -Name '`+name+`' -NewName '`+renamed+`' -PassThru | Select-Object -Property Name, AliasCount`)); len(got) != 1 || got[0] != (renamedTemplate{renamed, 1}) {
		t.Fatalf("rename: %+v", got)
	}
	if got := w.do(`(Get-PagerArchiveAlias -Pattern 0100004).Template`); len(got) != 1 || got[0] != renamed {
		t.Errorf("Station 4's template after the rename: %q", got)
	}
	if agency, _ := owner.newestAgencyAndIcon("0100004"); agency != "Example Rescue Service" {
		t.Errorf("0100004's page shows agency %q after the rename", agency)
	}
	out := w.do(`Rename-PaTemplate -Name '` + renamed + `' -NewName 'Example Fire'`)
	want := `ERROR template_exists,Rename-PagerArchiveTemplate There's already a template called "Example Fire".`
	if len(out) != 1 || !strings.HasPrefix(out[0], want) {
		t.Errorf("rename to a taken name: %q, want %q", out, want)
	}
	if owner.template(renamed).Aliases != 1 {
		t.Errorf("%s changed after the refused rename", renamed)
	}
	if out := w.do(`Rename-PagerArchiveTemplate -Name '` + renamed + `' -NewName '` + name + `'`); len(out) != 0 {
		t.Fatalf("rename back: %q", out)
	}
	t.Logf("A56: renamed %q to %q and back, Station 4 kept it; a taken name: %q", name, renamed, out)

	// Deleting "Example Fire" is refused, saying how many aliases use it.
	count := strings.Join(w.do(`(Get-PagerArchiveTemplate -Name 'Example Fire').AliasCount`), "")
	out = w.do(`Remove-PagerArchiveTemplate -Name 'Example Fire' -Confirm:$false`)
	want = fmt.Sprintf(`ERROR template_in_use,Remove-PagerArchiveTemplate "Example Fire" can't be deleted: %s aliases use it.`, count)
	if len(out) != 1 || !strings.HasPrefix(out[0], want) {
		t.Errorf("delete Example Fire: %q, want %q", out, want)
	}
	t.Logf("A56: %q", out)

	// User B can't change a template through the API.
	b := ps.inviteAndEnrol(t, owner, "ps"+ps.prefix+"-m10b-"+ps.run+"@example.com")
	fire := owner.template("Example Fire")
	path := fmt.Sprintf("/api/v1/admin/templates/%d", fire.ID)
	if code, _ := b.api(http.MethodPatch, path, map[string]string{"agency": "Changed by B"}, nil); code != http.StatusForbidden {
		t.Errorf("user B edits a template: %d", code)
	}
	req, _ := http.NewRequest(http.MethodPut, ps.app.JoinPath(path, "icon").String(), bytes.NewReader([]byte("x")))
	resp, err := b.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("user B uploads an icon: %d", resp.StatusCode)
	}
	if after := owner.template("Example Fire"); after.Agency != fire.Agency || deref(after.Icon) != deref(fire.Icon) {
		t.Errorf("Example Fire changed: %+v", after)
	}
	t.Logf("A56: user B refused (403) editing a template and uploading an icon")
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// A58. An icon added with the module is a fresh 64×64 PNG without the
// file's text; one from a URL is downloaded by the module, and still shows
// after the web server has stopped.
func TestPowerShellA58IconsStoredNotLinked(t *testing.T) {
	ps := setupPS(t)
	owner := ps.owner(t)
	w := ps.window(t, ps.psHome(t))
	ps.connect(w, owner, "")
	stations(t, w)
	run := ps.prefix + ps.run

	// A PNG carrying a text chunk "NOT-KEPT".
	name := "Not kept " + run
	in, err := os.ReadFile(testIconPath(t, "not-kept.png"))
	if err != nil || !bytes.Contains(in, []byte("NOT-KEPT")) {
		t.Fatalf("not-kept.png: %v", err)
	}
	out := w.do(`New-PagerArchiveTemplate -Name '` + name + `' -Agency 'Example Kept Service' -Path '` + testIconPath(t, "not-kept.png") + `' | Select-Object -ExpandProperty HasIcon`)
	if strings.Join(out, "") != "True" {
		t.Fatalf("New-PagerArchiveTemplate -Path: %q", out)
	}
	t.Cleanup(func() { w.do(`Remove-PagerArchiveTemplate -Name '` + name + `' -Confirm:$false`) })
	resp, stored := owner.get(deref(owner.template(name).Icon))
	c, err := png.DecodeConfig(bytes.NewReader(stored))
	if resp.StatusCode != http.StatusOK || err != nil || c.Width != 64 || c.Height != 64 {
		t.Fatalf("stored icon: %d %+v %v", resp.StatusCode, c, err)
	}
	if bytes.Contains(stored, []byte("NOT-KEPT")) || bytes.Contains(stored, []byte("tEXt")) {
		t.Error("the stored icon kept the file's text")
	}
	t.Logf("A58: not-kept.png (%d bytes, with a tEXt chunk) stored as a %d-byte 64×64 PNG without it", len(in), len(stored))

	// From a URL: a test web server serving the test icon, stopped once the
	// module has set it.
	file, err := os.ReadFile(testIconPath(t, "example-icon.png"))
	if err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int64
	var credentials atomic.Bool
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		credentials.Store(credentials.Load() || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "")
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(file)
	}))
	out = w.do(`Set-PagerArchiveTemplate -Name 'Example Fire' -Uri '` + site.URL + `/example-icon.png'`)
	site.Close()
	if len(out) != 0 || hits.Load() != 1 || credentials.Load() {
		t.Fatalf("Set-PagerArchiveTemplate -Uri: %q, %d requests, credentials sent: %v", out, hits.Load(), credentials.Load())
	}
	ps.postPage(t, "0100001", "@@EX"+run+"58 HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON #STN1")
	agency, url := owner.newestAgencyAndIcon("0100001")
	if agency != "Example Fire Service" || url == "" || url != deref(owner.template("Example Fire").Icon) {
		t.Fatalf("the message list shows agency %q, icon %q", agency, url)
	}
	resp, served := owner.get(url)
	want, err := icon.Convert(file)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "image/png" || !bytes.Equal(served, want) {
		t.Errorf("served icon: %d %s, %d bytes, the same as converting the file here: %v", resp.StatusCode, resp.Header.Get("Content-Type"), len(served), bytes.Equal(served, want))
	}
	t.Logf("A58: -Uri downloaded once by the module (no credentials), the site stopped, and the list's page shows %s (%d bytes)", url, len(served))

	// -RemoveIcon, and setting it again from the file.
	if out := w.do(`Set-PagerArchiveTemplate -Name 'Example Fire' -RemoveIcon; (Get-PagerArchiveTemplate -Name 'Example Fire').HasIcon`); strings.Join(out, "") != "False" {
		t.Errorf("-RemoveIcon: %q", out)
	}
	if _, url := owner.newestAgencyAndIcon("0100001"); url != "" {
		t.Errorf("after -RemoveIcon the list shows %q", url)
	}
	if out := w.do(`Set-PagerArchiveTemplate -Name 'Example Fire' -LiteralPath '` + testIconPath(t, "example-icon.png") + `'; (Get-PagerArchiveTemplate -Name 'Example Fire').HasIcon`); strings.Join(out, "") != "True" {
		t.Errorf("-LiteralPath: %q", out)
	}
	// Refusals come back with the API's code.
	out = w.do(`Set-PagerArchiveTemplate -Name 'Example Fire' -Path '` + testIconPath(t, "not-an-image.png") + `'`)
	if len(out) != 1 || !strings.HasPrefix(out[0], "ERROR icon_type,Set-PagerArchiveTemplate Icons must be PNG, ICO, GIF, JPEG or WebP images") {
		t.Errorf("a text file renamed .png: %q", out)
	}
	t.Logf("A58: -RemoveIcon, -LiteralPath and a refusal: %q", out)
}
