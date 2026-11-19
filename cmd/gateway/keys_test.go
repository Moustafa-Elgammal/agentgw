package main

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/internal/connector"
	"github.com/Moustafa-Elgammal/agentgw/internal/obs"
	"github.com/Moustafa-Elgammal/agentgw/internal/studiosig"
	"github.com/Moustafa-Elgammal/agentgw/pkg/agentsig"
)

func signedWith(id string, secret []byte) *http.Request {
	r := httptest.NewRequest("GET", "/v1/studio/x", nil)
	agentsig.Sign(r, id, secret, "alice", id+time.Now().String(), nil, time.Now())
	return r
}

func accepts(v *studiosig.Verifier, id string, secret []byte) bool {
	_, _, err := v.Verify(httptest.NewRecorder(), signedWith(id, secret))
	return err == nil
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// reloadKeys covers both modes: the flag key is added to the file's keys, or retired by "only".
func TestReloadKeysModes(t *testing.T) {
	tests := []struct {
		name        string
		only        bool
		wantPrimary bool
	}{
		{"default keeps the flag key", false, true},
		{"only retires the flag key", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			file := filepath.Join(dir, "keys.json")
			writeFile(t, file, `{"a":"secret-a"}`)
			var logs bytes.Buffer
			log := slog.New(slog.NewJSONHandler(&logs, nil))
			reg := obs.NewRegistry()
			reloads := obs.NewKeyReloads(reg)
			v := studiosig.New(studiosig.Config{})
			var reloaders []func()
			if !reloadKeys(log, v, "studio", "primary", []byte("secret-p"), file, tt.only, reloads, &reloaders) {
				t.Fatal("first load failed")
			}
			if got := accepts(v, "primary", []byte("secret-p")); got != tt.wantPrimary {
				t.Fatalf("primary accepted = %v, want %v", got, tt.wantPrimary)
			}
			if !accepts(v, "a", []byte("secret-a")) {
				t.Fatal("file key a not accepted")
			}

			// Overlap, then retire a, with SIGHUP each time.
			writeFile(t, file, `{"a":"secret-a","b":"secret-b"}`)
			for _, r := range reloaders {
				r()
			}
			if !accepts(v, "a", []byte("secret-a")) || !accepts(v, "b", []byte("secret-b")) {
				t.Fatal("both keys must be accepted during the overlap")
			}
			writeFile(t, file, `{"b":"secret-b"}`)
			for _, r := range reloaders {
				r()
			}
			if accepts(v, "a", []byte("secret-a")) {
				t.Fatal("retired key a still accepted")
			}
			if !accepts(v, "b", []byte("secret-b")) {
				t.Fatal("key b lost")
			}

			// A bad file keeps the keys in use and counts as an error.
			writeFile(t, file, `not json`)
			for _, r := range reloaders {
				r()
			}
			if !accepts(v, "b", []byte("secret-b")) {
				t.Fatal("a bad file must keep the current keys")
			}

			out := logs.String()
			for _, want := range []string{`"added":["b"]`, `"removed":["a"]`, `"primary_retired":` + map[bool]string{true: "true", false: "false"}[tt.only]} {
				if !strings.Contains(out, want) {
					t.Errorf("log lacks %s:\n%s", want, out)
				}
			}
			for _, secret := range []string{"secret-p", "secret-a", "secret-b"} {
				if strings.Contains(out, secret) {
					t.Errorf("a secret leaked into the log: %s", secret)
				}
			}
			var m bytes.Buffer
			_, _ = reg.WriteTo(&m)
			text := m.String()
			if !strings.Contains(text, `agentgw_key_reloads_total{kind="studio",result="ok"} 3`) ||
				!strings.Contains(text, `agentgw_key_reloads_total{kind="studio",result="error"} 1`) {
				t.Errorf("reload metric wrong:\n%s", text)
			}
		})
	}
}

func TestReloadKeysOnlyWithoutAFileFails(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	v := studiosig.New(studiosig.Config{})
	var reloaders []func()
	if reloadKeys(log, v, "studio", "primary", []byte("p"), "", true, nil, &reloaders) {
		t.Fatal("only with no file must not load an empty key set")
	}
}

func TestReloadBackendKey(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "backend.json")
	writeFile(t, file, `{"id":"b-1","secret":"one"}`)
	c := connector.NewHTTP("http://127.0.0.1:1", "dev-1", []byte("boot"))
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	var reloaders []func()
	reg := obs.NewRegistry()
	if !reloadBackendKey(log, c, file, obs.NewKeyReloads(reg), &reloaders) {
		t.Fatal("first load failed")
	}
	if c.CurrentKeyID() != "b-1" {
		t.Fatalf("key = %s", c.CurrentKeyID())
	}
	writeFile(t, file, `{"id":"b-2","secret":"two"}`)
	reloaders[0]()
	if c.CurrentKeyID() != "b-2" {
		t.Fatalf("after reload key = %s", c.CurrentKeyID())
	}
	writeFile(t, file, `{"id":"","secret":"x"}`)
	reloaders[0]()
	if c.CurrentKeyID() != "b-2" {
		t.Fatalf("a bad file must keep b-2, got %s", c.CurrentKeyID())
	}
	out := logs.String()
	if !strings.Contains(out, `"key_id":"b-2"`) || !strings.Contains(out, `"previous_key_id":"b-1"`) {
		t.Errorf("log lacks the key ids:\n%s", out)
	}
	if strings.Contains(out, "one") && strings.Contains(out, `"secret"`) {
		t.Errorf("secret in log:\n%s", out)
	}
	var m bytes.Buffer
	_, _ = reg.WriteTo(&m)
	if !strings.Contains(m.String(), `agentgw_key_reloads_total{kind="backend",result="error"} 1`) {
		t.Errorf("metric:\n%s", m.String())
	}
}
