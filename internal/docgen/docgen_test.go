package docgen

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/Moustafa-Elgammal/agentgw/internal/core"
	"github.com/Moustafa-Elgammal/agentgw/internal/manifest"
)

func root(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	r, err := repoRoot(wd)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// The committed pages must equal what the generators produce now.
func TestGeneratedFilesAreUpToDate(t *testing.T) {
	r := root(t)
	files, err := Files(r)
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range files {
		got, err := os.ReadFile(filepath.Join(r, path)) //nolint:gosec // path comes from the generator list
		if err != nil {
			t.Errorf("%s is missing: run `make docs-gen` (docker compose run --rm dev make docs-gen)", path)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s is out of date: run `make docs-gen` (docker compose run --rm dev make docs-gen) and commit the result", path)
		}
	}
}

func TestGeneratedTextRules(t *testing.T) {
	files, err := Files(root(t))
	if err != nil {
		t.Fatal(err)
	}
	for path, data := range files {
		if strings.ContainsRune(string(data), 0x2014) || strings.ContainsRune(string(data), 0x2013) {
			t.Errorf("%s contains a long dash", path)
		}
		if strings.HasSuffix(path, ".md") && !strings.HasPrefix(string(data), Header) {
			t.Errorf("%s does not start with the generated-file header", path)
		}
	}
}

func TestEveryTierIsValidAndDocumented(t *testing.T) {
	if len(tierText) != len(core.Tiers) {
		t.Fatalf("tierText has %d entries, core.Tiers has %d", len(tierText), len(core.Tiers))
	}
	for _, tier := range core.Tiers {
		if !tier.Valid() {
			t.Errorf("core.Tiers lists %q but Tier.Valid rejects it", tier)
		}
	}
	// The validator must not accept a tier the docs do not list.
	for _, bad := range []core.Tier{"", "admin", "action", "READ"} {
		if bad.Valid() {
			t.Errorf("Tier %q is valid but not in core.Tiers", bad)
		}
	}
}

func TestManifestRulesMatchValidator(t *testing.T) {
	re := regexp.MustCompile(manifest.NamePattern())
	for _, n := range []string{"ab", "get_city_state", "a" + strings.Repeat("b", 63)} {
		if !re.MatchString(n) {
			t.Errorf("%q should be a valid name", n)
		}
	}
	for _, n := range []string{"a", "Ab", "1ab", "a-b", "a" + strings.Repeat("b", 64)} {
		if re.MatchString(n) {
			t.Errorf("%q should not be a valid name", n)
		}
	}
}

// Every result code the source emits must be in core.Codes, and every code in the registry
// must still be emitted.
func TestResultCodesMatchSource(t *testing.T) {
	r := root(t)
	emit := regexp.MustCompile(`(?:Denied|deniedPtr|Deny)\(\s*"([a-z_]+)"|\bCode\s*(?::|=)\s*"([a-z_]+)"`)
	found := map[string]string{} // code -> a file that emits it
	for _, dir := range []string{"internal", "cmd", "pkg"} {
		err := filepath.WalkDir(filepath.Join(r, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			name := d.Name()
			if d.IsDir() && (strings.HasSuffix(name, "test") || name == "docgen") {
				return filepath.SkipDir // contract suites and this package build fake codes
			}
			if d.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path) //nolint:gosec // walking our own source tree
			if err != nil {
				return err
			}
			for _, m := range emit.FindAllStringSubmatch(string(src), -1) {
				c := m[1] + m[2]
				rel, _ := filepath.Rel(r, path)
				found[c] = rel
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(found) < 20 {
		t.Fatalf("scan found only %d codes, the pattern is broken", len(found))
	}
	listed := map[string]bool{}
	for _, c := range core.Codes {
		if listed[c.Code] {
			t.Errorf("code %q is listed twice", c.Code)
		}
		listed[c.Code] = true
		if c.Source == "" || c.Meaning == "" || c.Action == "" {
			t.Errorf("code %q has an empty field", c.Code)
		}
	}
	var missing, stale []string
	for c, file := range found {
		if !listed[c] {
			missing = append(missing, c+" ("+file+")")
		}
	}
	for c := range listed {
		if _, ok := found[c]; !ok {
			stale = append(stale, c)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	if len(missing) > 0 {
		t.Errorf("result codes emitted by the source but missing from core.Codes (internal/core/codes.go): %v", missing)
	}
	if len(stale) > 0 {
		t.Errorf("codes in core.Codes that no source emits any more: %v", stale)
	}
}

func TestHTTPErrorCodesMatchSource(t *testing.T) {
	r := root(t)
	pat := regexp.MustCompile(`"error":\s*"([a-z_]+)"|fail\(w, http\.Status\w+, "([a-z_]+)"\)|oauthError\(w, http\.\w+, "([a-z_]+)"`)
	listed := map[string]bool{}
	for _, e := range core.HTTPErrors {
		listed[e.Code] = true
	}
	// invalid_client_metadata and friends are OAuth error codes defined by RFC 7591 and 6749.
	oauth := map[string]bool{"invalid_client_metadata": true, "invalid_redirect_uri": true, "invalid_grant": true, "unsupported_grant_type": true, "invalid_scope": true, "invalid_target": true, "access_denied": true, "server_error": true, "unauthorized_client": true, "unsupported_response_type": true}
	for _, pkg := range []string{"manifeststore", "activity", "relay", "confirm", "auth"} {
		dir := filepath.Join(r, "internal", pkg)
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			src, _ := os.ReadFile(filepath.Join(dir, e.Name())) //nolint:gosec // our own source tree
			for _, m := range pat.FindAllStringSubmatch(string(src), -1) {
				c := m[1] + m[2] + m[3]
				if !listed[c] && !oauth[c] {
					t.Errorf("%s/%s returns error %q which is not in core.HTTPErrors", pkg, e.Name(), c)
				}
			}
		}
	}
}
