package manifeststore_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Moustafa-Elgammal/agentgw/internal/manifest"
	"github.com/Moustafa-Elgammal/agentgw/internal/manifeststore"
)

const jsonA = `{"version":1,"tools":[{"name":"get_state","description":"State.","tier":"read","http":{"method":"GET","url":"/state"}}]}`
const yamlA = `
version: 1
tools:
  - name: get_state
    description: State.
    tier: read
    http: {method: GET, url: /state}
`
const jsonB = `{"version":1,"tools":[
{"name":"get_state","description":"State.","tier":"read","http":{"method":"GET","url":"/state"}},
{"name":"collect","description":"Collect.","tier":"safe","http":{"method":"POST","url":"/collect"}}]}`

type applier struct{ m *manifest.Manifest }

func (a *applier) SetManifest(m *manifest.Manifest) { a.m = m }
func (a *applier) tools() int {
	if a.m == nil {
		return 0
	}
	return len(a.m.Tools)
}

func TestUploadJSONAndYAMLSameHash(t *testing.T) {
	ctx := context.Background()
	m := manifeststore.NewManager(manifeststore.NewMemory(nil), "t", &applier{})
	a, err := m.Upload(ctx, "alice", []byte(jsonA), manifeststore.JSON)
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Upload(ctx, "bob", []byte(yamlA), manifeststore.YAML)
	if err != nil {
		t.Fatal(err)
	}
	if a.SHA256 != b.SHA256 || a.ID == b.ID || !b.Active || a.Actor != "alice" {
		t.Errorf("a=%+v b=%+v", a, b)
	}
}

func TestInvalidUploadChangesNothing(t *testing.T) {
	ctx := context.Background()
	ap := &applier{}
	st := manifeststore.NewMemory(nil)
	m := manifeststore.NewManager(st, "t", ap)
	if _, err := m.Upload(ctx, "a", []byte(jsonA), manifeststore.JSON); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{"version":2,"tools":[]}`, `not json`} {
		if _, err := m.Upload(ctx, "a", []byte(bad), manifeststore.JSON); !errors.Is(err, manifeststore.ErrInvalid) {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
	vs, _ := st.List(ctx, "t")
	if len(vs) != 1 || ap.tools() != 1 {
		t.Errorf("versions=%d tools=%d", len(vs), ap.tools())
	}
}

func TestRollback(t *testing.T) {
	ctx := context.Background()
	ap := &applier{}
	st := manifeststore.NewMemory(nil)
	m := manifeststore.NewManager(st, "t", ap)
	v1, _ := m.Upload(ctx, "a", []byte(jsonA), manifeststore.JSON)
	if _, err := m.Upload(ctx, "a", []byte(jsonB), manifeststore.JSON); err != nil || ap.tools() != 2 {
		t.Fatal(err, ap.tools())
	}
	if _, err := m.Rollback(ctx, v1.ID); err != nil || ap.tools() != 1 {
		t.Fatalf("rollback: %v tools=%d", err, ap.tools())
	}
	if a, _, _ := st.Active(ctx, "t"); a.ID != v1.ID {
		t.Errorf("active = %d", a.ID)
	}
	if _, err := m.Rollback(ctx, 9999); !errors.Is(err, manifeststore.ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
	// A stored document that no longer validates must not be activated.
	bad, _ := st.Put(ctx, "t", "x", []byte(`{"version":9,"tools":[]}`), 0)
	if _, err := m.Rollback(ctx, bad.ID); !errors.Is(err, manifeststore.ErrInvalid) {
		t.Errorf("invalid stored: %v", err)
	}
	if a, _, _ := st.Active(ctx, "t"); a.ID != v1.ID || ap.tools() != 1 {
		t.Error("failed rollback changed state")
	}
}

func TestBootstrapSeedsOnlyWhenEmpty(t *testing.T) {
	ctx := context.Background()
	st := manifeststore.NewMemory(nil)
	ap := &applier{}
	m := manifeststore.NewManager(st, "t", ap)
	v, seeded, err := m.Bootstrap(ctx, "bootstrap", []byte(jsonA), manifeststore.JSON)
	if err != nil || !seeded || ap.tools() != 1 || v.Actor != "bootstrap" {
		t.Fatalf("first: %+v %v %v", v, seeded, err)
	}
	// Store already has an active version: the file is ignored, the store's version is applied.
	if _, err := m.Upload(ctx, "studio", []byte(jsonB), manifeststore.JSON); err != nil {
		t.Fatal(err)
	}
	ap2 := &applier{}
	m2 := manifeststore.NewManager(st, "t", ap2)
	_, seeded, err = m2.Bootstrap(ctx, "bootstrap", []byte(jsonA), manifeststore.JSON)
	vs, _ := st.List(ctx, "t")
	if err != nil || seeded || ap2.tools() != 2 || len(vs) != 2 {
		t.Errorf("second: seeded=%v err=%v tools=%d versions=%d", seeded, err, ap2.tools(), len(vs))
	}
}

func TestSyncPicksUpOtherManager(t *testing.T) {
	ctx := context.Background()
	st := manifeststore.NewMemory(nil)
	apA, apB := &applier{}, &applier{}
	a, b := manifeststore.NewManager(st, "t", apA), manifeststore.NewManager(st, "t", apB)
	if err := b.Sync(ctx); err != nil || apB.m != nil {
		t.Fatalf("empty sync: %v", err)
	}
	if _, err := a.Upload(ctx, "x", []byte(jsonB), manifeststore.JSON); err != nil {
		t.Fatal(err)
	}
	if err := b.Sync(ctx); err != nil || apB.tools() != 2 {
		t.Fatalf("sync: %v tools=%d", err, apB.tools())
	}
	// A stored doc that stopped validating leaves the running manifest alone.
	bad, _ := st.Put(ctx, "t", "x", []byte(strings.Repeat(" ", 3)), 0)
	_ = st.Activate(ctx, "t", bad.ID)
	if err := b.Sync(ctx); !errors.Is(err, manifeststore.ErrInvalid) || apB.tools() != 2 {
		t.Errorf("bad sync: %v tools=%d", err, apB.tools())
	}
}
