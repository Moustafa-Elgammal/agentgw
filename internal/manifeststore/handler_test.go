package manifeststore_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/internal/manifeststore"
	"github.com/Moustafa-Elgammal/agentgw/internal/studiosig"
	"github.com/Moustafa-Elgammal/agentgw/pkg/agentsig"
)

var adminSecret = []byte("admin-secret")

type rig struct {
	h  http.Handler
	n  int
	st *manifeststore.Memory
	ap *applier
}

func newRig() *rig {
	st := manifeststore.NewMemory(nil)
	ap := &applier{}
	v := studiosig.New(studiosig.Config{KeyID: "admin-1", Secret: adminSecret, MaxBody: manifeststore.MaxUpload})
	return &rig{h: manifeststore.Handler(manifeststore.NewManager(st, "t", ap), v), st: st, ap: ap}
}

// do sends a request signed with a fresh idempotency key unless idem is set.
func (r *rig) do(method, path, body, ctype, keyID, secret, idem string) *httptest.ResponseRecorder {
	r.n++
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if keyID != "" {
		if idem == "" {
			idem = "k" + strconv.Itoa(r.n)
		}
		agentsig.Sign(req, keyID, []byte(secret), "ops-alice", idem, []byte(body), time.Now())
	}
	w := httptest.NewRecorder()
	r.h.ServeHTTP(w, req)
	return w
}

func (r *rig) ok(method, path, body, ctype string) *httptest.ResponseRecorder {
	return r.do(method, path, body, ctype, "admin-1", string(adminSecret), "")
}

func TestAuthAndLimits(t *testing.T) {
	tests := []struct {
		name string
		do   func(r *rig) *httptest.ResponseRecorder
		want int
	}{
		{"unsigned", func(r *rig) *httptest.ResponseRecorder {
			return r.do("PUT", "/v1/studio/manifest", jsonA, "", "", "", "")
		}, 401},
		{"unknown key", func(r *rig) *httptest.ResponseRecorder {
			return r.do("PUT", "/v1/studio/manifest", jsonA, "", "other", string(adminSecret), "")
		}, 401},
		{"wrong secret", func(r *rig) *httptest.ResponseRecorder {
			return r.do("PUT", "/v1/studio/manifest", jsonA, "", "admin-1", "nope", "")
		}, 401},
		{"replay", func(r *rig) *httptest.ResponseRecorder {
			r.do("GET", "/v1/studio/manifest/versions", "", "", "admin-1", string(adminSecret), "same")
			return r.do("GET", "/v1/studio/manifest/versions", "", "", "admin-1", string(adminSecret), "same")
		}, 401},
		{"too large", func(r *rig) *httptest.ResponseRecorder {
			return r.ok("PUT", "/v1/studio/manifest", strings.Repeat("x", manifeststore.MaxUpload+1), "")
		}, 413},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig()
			if w := tt.do(r); w.Code != tt.want {
				t.Errorf("status %d, want %d: %s", w.Code, tt.want, w.Body)
			}
			if vs, _ := r.st.List(t.Context(), "t"); len(vs) != 0 {
				t.Error("a refused request stored a version")
			}
		})
	}
}

func TestUploadListActivate(t *testing.T) {
	r := newRig()
	w := r.ok("PUT", "/v1/studio/manifest", "version: 1\ntools: [bad]", "application/yaml")
	if w.Code != 422 || !strings.Contains(w.Body.String(), `"invalid_manifest"`) || !strings.Contains(w.Body.String(), "detail") {
		t.Fatalf("invalid: %d %s", w.Code, w.Body)
	}
	w = r.ok("PUT", "/v1/studio/manifest", yamlA, "application/yaml; charset=utf-8")
	var v1 manifeststore.Version
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &v1) != nil || v1.Actor != "ops-alice" || !v1.Active || v1.Tools != 1 {
		t.Fatalf("yaml upload: %d %s", w.Code, w.Body)
	}
	if w = r.ok("PUT", "/v1/studio/manifest", jsonB, "application/json"); w.Code != 201 || r.ap.tools() != 2 {
		t.Fatalf("json upload: %d tools=%d", w.Code, r.ap.tools())
	}

	w = r.ok("GET", "/v1/studio/manifest", "", "")
	var act struct {
		Version  manifeststore.Version
		Manifest struct{ Tools []any }
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &act) != nil || len(act.Manifest.Tools) != 2 || act.Version.ID == v1.ID {
		t.Fatalf("active: %d %s", w.Code, w.Body)
	}
	w = r.ok("GET", "/v1/studio/manifest/versions", "", "")
	var list struct{ Versions []manifeststore.Version }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list.Versions) != 2 ||
		list.Versions[0].ID < list.Versions[1].ID || !list.Versions[0].Active || list.Versions[1].Active {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}

	path := "/v1/studio/manifest/versions/" + strconv.FormatInt(v1.ID, 10) + "/activate"
	if w = r.ok("POST", path, "", ""); w.Code != 200 || r.ap.tools() != 1 {
		t.Fatalf("activate: %d tools=%d", w.Code, r.ap.tools())
	}
	for _, p := range []string{"/v1/studio/manifest/versions/999/activate", "/v1/studio/manifest/versions/abc/activate"} {
		if w = r.ok("POST", p, "", ""); w.Code != 404 {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
}

func TestActiveNotFoundWhenEmpty(t *testing.T) {
	if w := newRig().ok("GET", "/v1/studio/manifest", "", ""); w.Code != 404 || !bytes.Contains(w.Body.Bytes(), []byte("not_found")) {
		t.Errorf("%d %s", w.Code, w.Body)
	}
}
