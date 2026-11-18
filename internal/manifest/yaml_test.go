package manifest

import (
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
