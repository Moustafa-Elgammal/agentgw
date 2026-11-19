package docgen

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"

	"github.com/Moustafa-Elgammal/agentgw/internal/manifest"
)

// A small JSON Schema checker: enough of draft 2020-12 for the generated manifest schema.
type checker struct{ root map[string]any }

func (c checker) ref(s map[string]any) map[string]any {
	r, ok := s["$ref"].(string)
	if !ok {
		return s
	}
	cur := any(c.root)
	for _, p := range strings.Split(strings.TrimPrefix(r, "#/"), "/") {
		cur = cur.(map[string]any)[p]
	}
	return cur.(map[string]any)
}

func (c checker) check(s map[string]any, v any, path string) []string {
	s = c.ref(s)
	var errs []string
	bad := func(f string, a ...any) { errs = append(errs, path+": "+fmt.Sprintf(f, a...)) }
	if k, ok := s["const"]; ok && !reflect.DeepEqual(k, v) {
		bad("must equal %v", k)
	}
	if e, ok := s["enum"].([]any); ok {
		found := false
		for _, x := range e {
			if reflect.DeepEqual(x, v) {
				found = true
			}
		}
		if !found {
			bad("%v is not in the enum", v)
		}
	}
	if t, ok := s["type"].(string); ok && !typeOK(t, v) {
		bad("must be %s", t)
		return errs
	}
	switch x := v.(type) {
	case string:
		if p, ok := s["pattern"].(string); ok && !regexp.MustCompile(p).MatchString(x) {
			bad("does not match %s", p)
		}
		if n, ok := s["minLength"].(float64); ok && float64(len(x)) < n {
			bad("too short")
		}
		if n, ok := s["maxLength"].(float64); ok && float64(len(x)) > n {
			bad("too long")
		}
	case float64:
		if n, ok := s["minimum"].(float64); ok && x < n {
			bad("below minimum")
		}
		if n, ok := s["maximum"].(float64); ok && x > n {
			bad("above maximum")
		}
	case []any:
		if n, ok := s["minItems"].(float64); ok && float64(len(x)) < n {
			bad("too few items")
		}
		if it, ok := s["items"].(map[string]any); ok {
			for i, e := range x {
				errs = append(errs, c.check(it, e, fmt.Sprintf("%s[%d]", path, i))...)
			}
		}
	case map[string]any:
		for _, r := range asStrings(s["required"]) {
			if _, ok := x[r]; !ok {
				bad("missing %q", r)
			}
		}
		props, _ := s["properties"].(map[string]any)
		for k, e := range x {
			if ps, ok := props[k].(map[string]any); ok {
				errs = append(errs, c.check(ps, e, path+"."+k)...)
				continue
			}
			switch ap := s["additionalProperties"].(type) {
			case bool:
				if !ap {
					bad("unknown field %q", k)
				}
			case map[string]any:
				errs = append(errs, c.check(ap, e, path+"."+k)...)
			}
		}
		if n, ok := s["maxProperties"].(float64); ok && float64(len(x)) > n {
			bad("too many properties")
		}
	}
	if all, ok := s["allOf"].([]any); ok {
		for _, sub := range all {
			errs = append(errs, c.check(sub.(map[string]any), v, path)...)
		}
	}
	if alts, ok := s["anyOf"].([]any); ok {
		okAny := false
		for _, sub := range alts {
			if len(c.check(sub.(map[string]any), v, path)) == 0 {
				okAny = true
			}
		}
		if !okAny {
			bad("matches none of anyOf")
		}
	}
	if cond, ok := s["if"].(map[string]any); ok {
		if len(c.check(cond, v, path)) == 0 {
			if then, ok := s["then"].(map[string]any); ok {
				errs = append(errs, c.check(then, v, path)...)
			}
		} else if els, ok := s["else"].(map[string]any); ok {
			errs = append(errs, c.check(els, v, path)...)
		}
	}
	return errs
}

func asStrings(v any) []string {
	var out []string
	l, _ := v.([]any)
	for _, x := range l {
		out = append(out, x.(string))
	}
	return out
}

func typeOK(t string, v any) bool {
	switch t {
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "string":
		_, ok := v.(string)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "number":
		_, ok := v.(float64)
		return ok
	case "integer":
		f, ok := v.(float64)
		return ok && f == math.Trunc(f)
	}
	return false
}

func schemaChecker(t *testing.T) checker {
	t.Helper()
	raw, err := ManifestSchema()
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	return checker{root: root}
}

func tool(extra string) string {
	return `{"version":1,"tools":[{"name":"ab","description":"d",` + extra + `}]}`
}

// The schema and the Go validator must agree on every rule a schema can express.
func TestSchemaAgreesWithValidator(t *testing.T) {
	c := schemaChecker(t)
	get := `"http":{"method":"GET","url":"/x"}`
	tests := []struct {
		name string
		doc  string
		ok   bool
	}{
		{"http read tool", tool(`"tier":"read",` + get), true},
		{"safe with cooldown", tool(`"tier":"safe","caps":{"cooldown_seconds":5},` + get), true},
		{"medium with confirm", tool(`"tier":"medium","confirm":true,` + get), true},
		{"amount cap with param", tool(`"tier":"medium","caps":{"per_day_amount":9,"amount_param":"q"},` + get + `,"params":{"q":{"type":"integer","min":1,"max":5}}`), true},
		{"enum string param", tool(`"tier":"read",` + get + `,"params":{"k":{"type":"string","enum":["a","b"],"optional":true}}`), true},
		{"relay without http", tool(`"tier":"medium","backend":"relay"`), true},
		{"relay with empty http", tool(`"tier":"medium","backend":"relay","http":{}`), true},
		{"relay with blank http", tool(`"tier":"medium","backend":"relay","http":{"method":"","url":""}`), true},
		{"relay with http", tool(`"tier":"medium","backend":"relay",` + get), false},
		{"http without binding", tool(`"tier":"read"`), false},
		{"explicit http without binding", tool(`"tier":"read","backend":"http"`), false},
		{"unknown backend", tool(`"tier":"read","backend":"ftp",` + get), false},
		{"unknown tier", tool(`"tier":"root",` + get), false},
		{"read with confirm", tool(`"tier":"read","confirm":true,` + get), false},
		{"read with daily calls", tool(`"tier":"read","caps":{"per_day_calls":2},` + get), false},
		{"amount cap without param name", tool(`"tier":"medium","caps":{"per_day_amount":9},` + get), false},
		{"negative cap", tool(`"tier":"safe","caps":{"cooldown_seconds":-1},` + get), false},
		{"enum on integer", tool(`"tier":"read",` + get + `,"params":{"k":{"type":"integer","enum":["a"]}}`), false},
		{"min on string", tool(`"tier":"read",` + get + `,"params":{"k":{"type":"string","min":1}}`), false},
		{"unknown param type", tool(`"tier":"read",` + get + `,"params":{"k":{"type":"date"}}`), false},
		{"unknown field", tool(`"tier":"read","oops":1,` + get), false},
		{"bad method", tool(`"tier":"read","http":{"method":"TRACE","url":"/x"}`), false},
		{"relative url", tool(`"tier":"read","http":{"method":"GET","url":"x"}`), false},
		{"blank description", `{"version":1,"tools":[{"name":"ab","description":"  ","tier":"read",` + get + `}]}`, false},
		{"bad name", `{"version":1,"tools":[{"name":"Ab","description":"d","tier":"read",` + get + `}]}`, false},
		{"version 2", `{"version":2,"tools":[{"name":"ab","description":"d","tier":"read",` + get + `}]}`, false},
		{"no tools", `{"version":1,"tools":[]}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, perr := manifest.Parse(strings.NewReader(tt.doc))
			if (perr == nil) != tt.ok {
				t.Fatalf("validator: err = %v, want ok=%v (fix the case)", perr, tt.ok)
			}
			var v any
			if err := json.Unmarshal([]byte(tt.doc), &v); err != nil {
				t.Fatal(err)
			}
			errs := c.check(c.root, v, "$")
			if (len(errs) == 0) != tt.ok {
				t.Fatalf("schema disagrees with the validator (validator ok=%v): %v", tt.ok, errs)
			}
		})
	}
}

func TestSchemaAcceptsExampleManifests(t *testing.T) {
	c := schemaChecker(t)
	r := root(t)
	jsonDoc, err := os.ReadFile(filepath.Join(r, "examples/city/manifest.json")) //nolint:gosec // fixed example path
	if err != nil {
		t.Fatal(err)
	}
	yamlDoc, err := os.ReadFile(filepath.Join(r, "examples/city/manifest.yaml")) //nolint:gosec // fixed example path
	if err != nil {
		t.Fatal(err)
	}
	toJSON := func(y []byte) []byte {
		t.Helper()
		var v any
		if err := yaml.Unmarshal(y, &v); err != nil {
			t.Fatal(err)
		}
		j, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return j
	}
	asJSON := toJSON(yamlDoc)
	for name, doc := range map[string][]byte{"manifest.json": jsonDoc, "manifest.yaml": asJSON, "reference example": toJSON([]byte(exampleManifest))} {
		var v any
		if err := json.Unmarshal(doc, &v); err != nil {
			t.Fatal(name, err)
		}
		if errs := c.check(c.root, v, "$"); len(errs) > 0 {
			t.Errorf("%s: %v", name, errs)
		}
		if _, err := manifest.Parse(strings.NewReader(string(doc))); err != nil {
			t.Errorf("%s fails the validator: %v", name, err)
		}
	}
}
