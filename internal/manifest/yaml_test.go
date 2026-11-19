package manifest

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The YAML authoring format must compile to exactly the structure JSON does.
func TestYAMLCityExampleEqualsJSON(t *testing.T) {
	j, err := LoadFile("../../examples/city/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	y, err := LoadFile("../../examples/city/manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(j, y) {
		jb, _ := j.Marshal()
		yb, _ := y.Marshal()
		t.Fatalf("yaml and json manifests differ\njson: %s\nyaml: %s", jb, yb)
	}
}

// Every manifest under examples/ (manifest.json, manifest.yaml, manifest.yml, or a name ending
// in one of those) must validate, and a JSON and YAML file that share a base name in one
// directory must describe the same manifest. YAML is the authoring format, so examples ship
// YAML first and may keep a JSON twin.
func TestExampleManifests(t *testing.T) {
	root := "../../examples"
	byBase := map[string]map[string]*Manifest{} // dir/base -> ext -> manifest
	found := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		ext := strings.ToLower(filepath.Ext(path))
		name := strings.TrimSuffix(d.Name(), filepath.Ext(path))
		if ext != ".json" && ext != ".yaml" && ext != ".yml" {
			return nil
		}
		if name != "manifest" && !strings.HasSuffix(name, "-lite") {
			return nil // other JSON or YAML files (compose files, keys) are not manifests
		}
		found++
		m, err := LoadFile(path)
		if err != nil {
			t.Errorf("%s does not validate: %v", path, err)
			return nil
		}
		key := filepath.Join(filepath.Dir(path), name)
		if byBase[key] == nil {
			byBase[key] = map[string]*Manifest{}
		}
		byBase[key][strings.TrimPrefix(strings.ReplaceAll(ext, ".yml", ".yaml"), ".")] = m
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found == 0 {
		t.Fatal("no example manifests found; is the path right?")
	}
	for key, exts := range byBase {
		j, y := exts["json"], exts["yaml"]
		if j == nil || y == nil {
			continue
		}
		if !reflect.DeepEqual(j, y) {
			jb, _ := j.Marshal()
			yb, _ := y.Marshal()
			t.Errorf("%s: json and yaml differ\njson: %s\nyaml: %s", key, jb, yb)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "city", "manifest.yaml")); err != nil {
		t.Errorf("the YAML city example is required: %v", err)
	}
}

const yamlGood = `
version: 1
tools:
  - name: get_state
    description: State.
    tier: read
    http: {method: GET, url: /state}
`

func TestParseYAMLRejects(t *testing.T) {
	tests := []struct {
		name, doc, want string
	}{
		{"unknown field", yamlGood + "    colour: red\n", "colour"},
		{"unknown top-level field", yamlGood + "extra: 1\n", "extra"},
		{"duplicate key", "version: 1\nversion: 1\ntools: []\n", "version"},
		{"wrong type", "version: one\ntools: []\n", "decode yaml"},
		{"semantic error still reported", strings.Replace(yamlGood, "tier: read", "tier: nuclear", 1), "unknown tier"},
		{"not a document", "- just\n- a list\n", "decode yaml"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseYAML(strings.NewReader(tc.doc))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestParseYAMLGood(t *testing.T) {
	m, err := ParseYAML(strings.NewReader(yamlGood))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Tools) != 1 || m.Tools[0].Name != "get_state" {
		t.Fatalf("unexpected manifest %+v", m)
	}
}
