package docgen

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-yaml"

	"github.com/Moustafa-Elgammal/agentgw/internal/activity"
	"github.com/Moustafa-Elgammal/agentgw/internal/audit"
	"github.com/Moustafa-Elgammal/agentgw/internal/auth"
	"github.com/Moustafa-Elgammal/agentgw/internal/confirm"
	"github.com/Moustafa-Elgammal/agentgw/internal/manifest"
	"github.com/Moustafa-Elgammal/agentgw/internal/manifeststore"
	"github.com/Moustafa-Elgammal/agentgw/internal/relay"
	"github.com/Moustafa-Elgammal/agentgw/internal/studiosig"
)

type noopApplier struct{}

func (noopApplier) SetManifest(*manifest.Manifest) {}

func allRoutes() []string {
	var all []string
	all = append(all, auth.Routes...)
	all = append(all, confirm.Routes...)
	all = append(all, manifeststore.Routes...)
	all = append(all, activity.Routes...)
	all = append(all, relay.Routes...)
	sort.Strings(all)
	return all
}

// docs/openapi/studio.yaml lists exactly the routes the handlers declare.
func TestOpenAPIMatchesHandlerRoutes(t *testing.T) {
	got, err := LoadRoutes(root(t))
	if err != nil {
		t.Fatal(err)
	}
	want := allRoutes()
	in := func(list []string, s string) bool {
		for _, x := range list {
			if x == s {
				return true
			}
		}
		return false
	}
	for _, w := range want {
		if !in(got, w) {
			t.Errorf("route %q is served but missing from docs/openapi/studio.yaml", w)
		}
	}
	for _, g := range got {
		if !in(want, g) {
			t.Errorf("docs/openapi/studio.yaml documents %q but no handler declares it", g)
		}
	}
}

// Each package's Routes list must equal the patterns written in its source, so a new
// HandleFunc cannot be added without listing it (and so documenting it).
func TestRoutesListsMatchSource(t *testing.T) {
	r := root(t)
	lit := regexp.MustCompile(`"((?:GET|POST|PUT|DELETE|PATCH) /[^"]*)"`)
	tests := []struct {
		pkg    string
		routes []string
	}{
		{"auth", auth.Routes},
		{"confirm", confirm.Routes},
		{"manifeststore", manifeststore.Routes},
		{"relay", relay.Routes},
	}
	for _, tt := range tests {
		t.Run(tt.pkg, func(t *testing.T) {
			dir := filepath.Join(r, "internal", tt.pkg)
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			found := map[string]bool{}
			for _, e := range entries {
				n := e.Name()
				if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") || n == "routes.go" {
					continue
				}
				src, _ := os.ReadFile(filepath.Join(dir, n)) //nolint:gosec // our own source tree
				for _, line := range strings.Split(string(src), "\n") {
					if !strings.Contains(line, "Handle") || strings.HasPrefix(strings.TrimSpace(line), "//") {
						continue
					}
					if m := lit.FindStringSubmatch(line); m != nil {
						found[m[1]] = true
					}
				}
			}
			listed := map[string]bool{}
			for _, x := range tt.routes {
				listed[x] = true
			}
			for f := range found {
				if !listed[f] {
					t.Errorf("internal/%s registers %q but Routes does not list it (add it to routes.go and docs/openapi/studio.yaml)", tt.pkg, f)
				}
			}
			for l := range listed {
				if !found[l] {
					t.Errorf("Routes lists %q but internal/%s does not register it", l, tt.pkg)
				}
			}
		})
	}
}

// Every listed route is really served: a request reaches a handler, not the mux's 404 or 405.
func TestListedRoutesAreServed(t *testing.T) {
	secret := []byte("s")
	v := studiosig.New(studiosig.Config{KeyID: "k", Secret: secret})
	as := auth.NewServer(auth.ServerConfig{Store: auth.NewMemory(nil), Issuer: "http://x", Resource: "http://x/mcp", Tenant: "t", StudioKeyID: "k", StudioSecret: secret, Verifier: v})
	mgr := manifeststore.NewManager(manifeststore.NewMemory(nil), "t", noopApplier{})
	hub := relay.NewHub(relay.NewMemory(nil))
	tickets := relay.NewTickets(secret, time.Minute, nil)
	store := &audit.Ring{}

	byPkg := []struct {
		name     string
		routes   []string
		handlers []http.Handler
	}{
		{"auth", auth.Routes, []http.Handler{as.Handler(), as.StudioHandler()}},
		{"confirm", confirm.Routes, []http.Handler{confirm.StudioHandler(confirm.StudioConfig{Store: confirm.NewMemory(nil), Tenant: "t", KeyID: "k", Secret: secret, Verifier: v})}},
		{"manifeststore", manifeststore.Routes, []http.Handler{manifeststore.Handler(mgr, v)}},
		{"activity", activity.Routes, []http.Handler{activity.PlayerHandler(store, "t", v), activity.AdminHandler(store, "t", v)}},
		{"relay", relay.Routes, []http.Handler{relay.Handler(hub, tickets), relay.MintHandler(tickets, v, "t")}},
	}
	for _, p := range byPkg {
		for _, route := range p.routes {
			method, path, _ := strings.Cut(route, " ")
			path = regexp.MustCompile(`\{[a-z]+\}`).ReplaceAllString(path, "1")
			served := false
			for _, h := range p.handlers {
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader("")))
				if rec.Code != http.StatusMethodNotAllowed && rec.Body.String() != "404 page not found\n" {
					served = true
				}
			}
			if !served {
				t.Errorf("%s: %q is listed in Routes but no handler serves it", p.name, route)
			}
		}
	}
}

// The spec is a real OpenAPI document: unique operation ids, resolvable refs, studio calls
// carry a signature scheme and the five signing headers.
func TestOpenAPIIsWellFormed(t *testing.T) {
	s, err := loadSpec(root(t))
	if err != nil {
		t.Fatal(err)
	}
	if v := str(get(s.root, "openapi")); !strings.HasPrefix(v, "3.1") {
		t.Fatalf("openapi = %q", v)
	}
	ids := map[string]bool{}
	for _, op := range s.Operations() {
		id := str(get(op.Node, "operationId"))
		if id == "" || ids[id] {
			t.Errorf("%s %s: operationId %q is empty or duplicated", op.Method, op.Path, id)
		}
		ids[id] = true
		if len(items(get(op.Node, "responses"))) == 0 {
			t.Errorf("%s %s has no responses", op.Method, op.Path)
		}
		if str(get(op.Node, "summary")) == "" || len(list(get(op.Node, "tags"))) == 0 {
			t.Errorf("%s %s needs a summary and a tag", op.Method, op.Path)
		}
		if strings.HasPrefix(op.Path, "/v1/studio/") {
			if len(list(get(op.Node, "security"))) == 0 {
				t.Errorf("%s %s is a signed call but declares no security", op.Method, op.Path)
			}
			have := map[string]bool{}
			for _, p := range list(get(op.Node, "parameters")) {
				have[str(get(s.resolve(p), "name"))] = true
			}
			for h := range signingHeaders {
				if !have[h] {
					t.Errorf("%s %s lacks the %s header parameter", op.Method, op.Path, h)
				}
			}
		}
		// every $ref resolves
		checkRefs(t, s, op.Node, op.Method+" "+op.Path)
	}
}

func checkRefs(t *testing.T, s *spec, n any, where string) {
	t.Helper()
	switch v := n.(type) {
	case yaml.MapSlice:
		if ref := str(get(v, "$ref")); ref != "" && s.resolve(v) == nil {
			t.Errorf("%s: $ref %s does not resolve", where, ref)
		}
		for _, it := range v {
			checkRefs(t, s, it.Value, where)
		}
	case []any:
		for _, it := range v {
			checkRefs(t, s, it, where)
		}
	}
}
