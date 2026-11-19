package docs

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Moustafa-Elgammal/agentgw/internal/core"
)

var root = filepath.Join("..", "..")

func read(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel))) //nolint:gosec // repository file
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Every result code the gateway can return needs its own heading in the troubleshooting guide.
func TestTroubleshootingCoversEveryCode(t *testing.T) {
	doc := read(t, "docs/guides/troubleshooting.md")
	headings := map[string]bool{}
	h := regexp.MustCompile("(?m)^#{2,4} `([a-z_]+)`")
	for _, m := range h.FindAllStringSubmatch(doc, -1) {
		headings[m[1]] = true
	}
	for _, c := range core.Codes {
		if !headings[c.Code] {
			t.Errorf("docs/guides/troubleshooting.md has no heading for the code %q: add a \"### `%s`\" section", c.Code, c.Code)
		}
	}
	for code := range headings {
		if _, ok := core.LookupCode(code); ok {
			continue
		}
		known := false
		for _, e := range core.HTTPErrors {
			known = known || e.Code == code
		}
		if !known {
			t.Errorf("docs/guides/troubleshooting.md has a heading for %q, which is in neither core.Codes nor core.HTTPErrors", code)
		}
	}
}

var emDash = regexp.MustCompile("[—–]")

func listFiles(t *testing.T, pattern string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(pattern)))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// Tutorials are written as a guide plus a script that CI runs. This keeps the pair together.
// A tutorial that is not written yet is logged, not failed, so the guides can land one by one.
func TestTutorialsAreComplete(t *testing.T) {
	planned := []string{
		"01-play-the-example-game", "02-describe-your-actions", "03-accept-signed-calls",
		"04-link-a-player", "05-ask-before-big-actions", "06-go-live", "07-test-your-integration", "08-sign-in-with-your-identity-provider",
	}
	for _, name := range planned {
		md := filepath.Join(root, "docs", "tutorials", name+".md")
		if _, err := os.Stat(md); err != nil {
			t.Logf("tutorial %s is not written yet", name)
		}
	}
	for _, md := range listFiles(t, "docs/tutorials/[0-9][0-9]-*.md") {
		name := strings.TrimSuffix(filepath.Base(md), ".md")
		t.Run(name, func(t *testing.T) {
			sh := strings.TrimSuffix(md, ".md") + ".sh"
			if _, err := os.Stat(sh); err != nil {
				t.Errorf("%s has no script %s: every command in a tutorial must be run by CI", name, filepath.Base(sh))
			}
			text, _ := os.ReadFile(md) //nolint:gosec // repository file
			if !strings.Contains(string(text), "Verified against") {
				t.Errorf("%s must say which version it was verified against (a line starting with \"Verified against\")", name)
			}
			if emDash.Match(text) {
				t.Errorf("%s contains an em or en dash", name)
			}
		})
	}
	for _, sh := range listFiles(t, "docs/tutorials/[0-9][0-9]-*.sh") {
		if _, err := os.Stat(strings.TrimSuffix(sh, ".sh") + ".md"); err != nil {
			t.Errorf("%s has no guide next to it", filepath.Base(sh))
		}
	}
}

var mdLink = regexp.MustCompile(`\]\(([^)\s#]+)(#[^)\s]*)?\)`)

// The guides and tutorials link to each other and to the rest of docs/. A broken relative link
// shows up here before the site build. Links to tutorials that are not written yet are skipped.
func TestGuideLinksResolve(t *testing.T) {
	files := append(listFiles(t, "docs/guides/*.md"), listFiles(t, "docs/tutorials/*.md")...)
	if len(files) == 0 {
		t.Fatal("no guides found")
	}
	for _, f := range files {
		text, _ := os.ReadFile(f) //nolint:gosec // repository file
		if emDash.Match(text) {
			t.Errorf("%s contains an em or en dash", f)
		}
		for _, m := range mdLink.FindAllStringSubmatch(string(text), -1) {
			href := m[1]
			if strings.Contains(href, "://") || strings.HasPrefix(href, "mailto:") {
				continue
			}
			target := filepath.Join(filepath.Dir(f), filepath.FromSlash(href))
			if _, err := os.Stat(target); err == nil { //nolint:gosec // link targets come from repository docs
				continue
			}
			if strings.Contains(filepath.ToSlash(target), "docs/tutorials/") {
				t.Logf("%s links to %s, which is not written yet", filepath.Base(f), href)
				continue
			}
			t.Errorf("%s links to %s, which does not exist", f, href)
		}
	}
}
