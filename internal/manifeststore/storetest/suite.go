// Package storetest is the behavior contract every manifeststore.Store must pass.
package storetest

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/internal/manifeststore"
)

// Factory builds a fresh, empty store that reads time from now.
type Factory func(t *testing.T, now func() time.Time) manifeststore.Store

// Run runs the contract.
func Run(t *testing.T, f Factory) {
	clock := func() time.Time { return time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC) }
	ctx := context.Background()
	fresh := func(t *testing.T) manifeststore.Store {
		t.Helper()
		return f(t, clock)
	}

	t.Run("empty store", func(t *testing.T) {
		s := fresh(t)
		if _, _, err := s.Active(ctx, "t"); !errors.Is(err, manifeststore.ErrNotFound) {
			t.Errorf("Active on empty = %v", err)
		}
		if _, _, err := s.Get(ctx, "t", 1); !errors.Is(err, manifeststore.ErrNotFound) {
			t.Errorf("Get on empty = %v", err)
		}
		if err := s.Activate(ctx, "t", 1); !errors.Is(err, manifeststore.ErrNotFound) {
			t.Errorf("Activate on empty = %v", err)
		}
		if vs, err := s.List(ctx, "t"); err != nil || len(vs) != 0 {
			t.Errorf("List on empty = %v %v", vs, err)
		}
	})

	t.Run("put does not activate, round trips the document", func(t *testing.T) {
		s := fresh(t)
		doc := []byte(`{"version":1,"tools":[]}`)
		v, err := s.Put(ctx, "t", "alice", doc, 3)
		if err != nil {
			t.Fatal(err)
		}
		if v.ID == 0 || v.Actor != "alice" || v.Tools != 3 || v.SHA256 == "" || v.Active || !v.CreatedAt.Equal(clock()) {
			t.Errorf("unexpected version %+v", v)
		}
		if _, _, err := s.Active(ctx, "t"); !errors.Is(err, manifeststore.ErrNotFound) {
			t.Errorf("Put must not activate: %v", err)
		}
		got, gotDoc, err := s.Get(ctx, "t", v.ID)
		if err != nil || !bytes.Equal(gotDoc, doc) || got.ID != v.ID || got.SHA256 != v.SHA256 || got.Active {
			t.Errorf("Get = %+v %q %v", got, gotDoc, err)
		}
		v2, _ := s.Put(ctx, "t", "alice", doc, 3)
		if v2.SHA256 != v.SHA256 || v2.ID == v.ID {
			t.Errorf("same doc: same hash, new id; got %+v %+v", v, v2)
		}
	})

	t.Run("activate, switch, active flag, newest first", func(t *testing.T) {
		s := fresh(t)
		a, _ := s.Put(ctx, "t", "x", []byte(`{"a":1}`), 1)
		b, _ := s.Put(ctx, "t", "y", []byte(`{"b":2}`), 2)
		c, _ := s.Put(ctx, "t", "z", []byte(`{"c":3}`), 3)
		if err := s.Activate(ctx, "t", a.ID); err != nil {
			t.Fatal(err)
		}
		v, doc, err := s.Active(ctx, "t")
		if err != nil || v.ID != a.ID || !v.Active || string(doc) != `{"a":1}` {
			t.Fatalf("Active = %+v %q %v", v, doc, err)
		}
		if err := s.Activate(ctx, "t", c.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.Activate(ctx, "t", c.ID); err != nil {
			t.Errorf("re-activating must be idempotent: %v", err)
		}
		vs, err := s.List(ctx, "t")
		if err != nil || len(vs) != 3 {
			t.Fatalf("List = %v %v", vs, err)
		}
		if vs[0].ID != c.ID || vs[1].ID != b.ID || vs[2].ID != a.ID {
			t.Errorf("not newest first: %+v", vs)
		}
		for _, v := range vs {
			if v.Active != (v.ID == c.ID) {
				t.Errorf("active flag wrong on %+v", v)
			}
		}
		if g, _, _ := s.Get(ctx, "t", a.ID); g.Active {
			t.Error("Get must show old version inactive")
		}
		if g, _, _ := s.Get(ctx, "t", c.ID); !g.Active {
			t.Error("Get must show active version active")
		}
	})

	t.Run("activate unknown id leaves active alone", func(t *testing.T) {
		s := fresh(t)
		a, _ := s.Put(ctx, "t", "x", []byte(`{}`), 1)
		_ = s.Activate(ctx, "t", a.ID)
		if err := s.Activate(ctx, "t", a.ID+1000); !errors.Is(err, manifeststore.ErrNotFound) {
			t.Errorf("err = %v", err)
		}
		if v, _, err := s.Active(ctx, "t"); err != nil || v.ID != a.ID {
			t.Errorf("active changed: %+v %v", v, err)
		}
	})

	t.Run("tenants are isolated", func(t *testing.T) {
		s := fresh(t)
		a, _ := s.Put(ctx, "t1", "x", []byte(`{"t":1}`), 1)
		b, _ := s.Put(ctx, "t2", "x", []byte(`{"t":2}`), 1)
		if err := s.Activate(ctx, "t2", a.ID); !errors.Is(err, manifeststore.ErrNotFound) {
			t.Errorf("activating another tenant's id = %v", err)
		}
		if _, _, err := s.Active(ctx, "t2"); !errors.Is(err, manifeststore.ErrNotFound) {
			t.Errorf("t2 must stay without an active version: %v", err)
		}
		if _, _, err := s.Get(ctx, "t2", a.ID); !errors.Is(err, manifeststore.ErrNotFound) {
			t.Errorf("Get across tenants = %v", err)
		}
		_ = s.Activate(ctx, "t1", a.ID)
		_ = s.Activate(ctx, "t2", b.ID)
		v1, _, _ := s.Active(ctx, "t1")
		v2, _, _ := s.Active(ctx, "t2")
		if v1.ID != a.ID || v2.ID != b.ID {
			t.Errorf("active mixed up: %+v %+v", v1, v2)
		}
		if vs, _ := s.List(ctx, "t1"); len(vs) != 1 || vs[0].ID != a.ID {
			t.Errorf("t1 list = %+v", vs)
		}
	})

	t.Run("returned document is a copy", func(t *testing.T) {
		s := fresh(t)
		v, _ := s.Put(ctx, "t", "x", []byte(`{"k":1}`), 1)
		_, d, _ := s.Get(ctx, "t", v.ID)
		d[0] = 'X'
		if _, d2, _ := s.Get(ctx, "t", v.ID); string(d2) != `{"k":1}` {
			t.Errorf("stored doc was mutated: %q", d2)
		}
	})
}
