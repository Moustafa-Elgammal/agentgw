package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func write(t *testing.T, path, body string, at time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

func TestKeysetRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	write(t, path, `{"a":"sa"}`, t0)
	k := newKeyset("dev-1", "boot", path)

	tests := []struct {
		name  string
		setup func()
		id    string
		want  string
	}{
		{"bootstrap key works", func() {}, "dev-1", "boot"},
		{"file key works", func() {}, "a", "sa"},
		{"unknown key", func() {}, "zzz", ""},
		{"overlap: both keys", func() { write(t, path, `{"a":"sa","b":"sb"}`, t0.Add(time.Second)) }, "b", "sb"},
		{"overlap keeps the old key", func() {}, "a", "sa"},
		{"old key dropped", func() { write(t, path, `{"b":"sb"}`, t0.Add(2*time.Second)) }, "a", ""},
		{"new key stays", func() {}, "b", "sb"},
		{"a broken file keeps the keys", func() { write(t, path, `not json`, t0.Add(3*time.Second)) }, "b", "sb"},
		{"an empty secret is ignored", func() { write(t, path, `{"c":""}`, t0.Add(4*time.Second)) }, "c", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup()
			if got := string(k.secretFor(tt.id)); got != tt.want {
				t.Fatalf("secretFor(%q) = %q, want %q", tt.id, got, tt.want)
			}
		})
	}
}

func TestKeysetWithoutFileOrSecret(t *testing.T) {
	k := newKeyset("dev-1", "", "")
	if k.secretFor("dev-1") != nil {
		t.Fatal("an empty bootstrap secret must not accept anything")
	}
}
