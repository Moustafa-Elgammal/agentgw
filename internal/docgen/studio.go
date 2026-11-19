package docgen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"
)

// OpenAPI reads docs/openapi/studio.yaml. It keeps key order, so the generated page follows the
// order of the spec. A node is a yaml.MapSlice, a []any or a scalar.
type spec struct{ root yaml.MapSlice }

// loadSpec parses the OpenAPI file at root.
func loadSpec(root string) (*spec, error) {
	raw, err := os.ReadFile(filepath.Join(root, OpenAPIYAML)) //nolint:gosec // fixed path under the repo root
	if err != nil {
		return nil, err
	}
	return parseSpec(raw)
}

func parseSpec(raw []byte) (*spec, error) {
	var v any
	if err := yaml.UnmarshalWithOptions(raw, &v, yaml.UseOrderedMap()); err != nil {
		return nil, fmt.Errorf("parse %s: %w", OpenAPIYAML, err)
	}
	ms, ok := v.(yaml.MapSlice)
	if !ok {
		return nil, fmt.Errorf("%s: top level is not a mapping", OpenAPIYAML)
	}
	return &spec{root: ms}, nil
}

func get(n any, key string) any {
	if ms, ok := n.(yaml.MapSlice); ok {
		for _, it := range ms {
			if k, ok := it.Key.(string); ok && k == key {
				return it.Value
			}
		}
	}
	return nil
}

func str(n any) string {
	switch v := n.(type) {
	case nil:
		return ""
	case string:
		return v
	}
	return fmt.Sprint(n)
}

func items(n any) yaml.MapSlice {
	ms, _ := n.(yaml.MapSlice)
	return ms
}

func list(n any) []any {
	l, _ := n.([]any)
	return l
}

// resolve follows a local $ref ("#/components/...").
func (s *spec) resolve(n any) any {
	for i := 0; i < 8; i++ {
		ref := str(get(n, "$ref"))
		if ref == "" {
			return n
		}
		cur := any(s.root)
		for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
			cur = get(cur, part)
		}
		n = cur
	}
	return n
}

// Operation is one method on one path.
type Operation struct {
	Method, Path string
	Node         any
}

// Operations lists every operation in file order.
func (s *spec) Operations() []Operation {
	var out []Operation
	for _, p := range items(get(s.root, "paths")) {
		for _, m := range items(p.Value) {
			switch m.Key {
			case "get", "put", "post", "delete", "patch", "head", "options":
				out = append(out, Operation{Method: strings.ToUpper(str(m.Key)), Path: str(p.Key), Node: m.Value})
			}
		}
	}
	return out
}

// Routes returns the "METHOD /path" patterns of the spec, sorted.
func (s *spec) Routes() []string {
	var out []string
	for _, o := range s.Operations() {
		out = append(out, o.Method+" "+o.Path)
	}
	sort.Strings(out)
	return out
}

// LoadRoutes reads the spec at root and returns its routes, for the drift test.
func LoadRoutes(root string) ([]string, error) {
	s, err := loadSpec(root)
	if err != nil {
		return nil, err
	}
	return s.Routes(), nil
}

var signingHeaders = map[string]bool{
	"X-AG-Key-Id": true, "X-AG-Timestamp": true, "X-AG-Player": true,
	"X-AG-Idempotency-Key": true, "X-AG-Signature": true,
}

func schemaType(s *spec, n any) string {
	if ref := str(get(n, "$ref")); ref != "" {
		return "`" + ref[strings.LastIndex(ref, "/")+1:] + "`"
	}
	t := str(get(n, "type"))
	switch t {
	case "array":
		return "array of " + schemaType(s, get(n, "items"))
	case "":
		return "any"
	}
	if f := str(get(n, "format")); f != "" {
		t += " (" + f + ")"
	}
	if e := list(get(n, "enum")); len(e) > 0 {
		vals := make([]string, len(e))
		for i, v := range e {
			vals[i] = "`" + str(v) + "`"
		}
		t += ": " + strings.Join(vals, ", ")
	}
	return t
}

func propTable(s *spec, schema any) string {
	required := map[string]bool{}
	for _, r := range list(get(schema, "required")) {
		required[str(r)] = true
	}
	var rows [][]string
	for _, p := range items(get(schema, "properties")) {
		req := "no"
		if required[str(p.Key)] {
			req = "yes"
		}
		rows = append(rows, []string{code(str(p.Key)), schemaType(s, p.Value), req, str(get(p.Value, "description"))})
	}
	if len(rows) == 0 {
		return ""
	}
	return mdTable([]string{"Field", "Type", "Required", "Meaning"}, rows)
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// toJSON renders a node as indented JSON, keeping key order.
func toJSON(n any) string {
	var b bytes.Buffer
	writeJSON(&b, n, 0)
	return b.String()
}

func writeJSON(b *bytes.Buffer, n any, depth int) {
	pad := strings.Repeat("  ", depth+1)
	switch v := n.(type) {
	case yaml.MapSlice:
		if len(v) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteString("{\n")
		for i, it := range v {
			k, _ := json.Marshal(str(it.Key))
			b.WriteString(pad + string(k) + ": ")
			writeJSON(b, it.Value, depth+1)
			if i < len(v)-1 {
				b.WriteString(",")
			}
			b.WriteString("\n")
		}
		b.WriteString(strings.Repeat("  ", depth) + "}")
	case []any:
		if len(v) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteString("[\n")
		for i, it := range v {
			b.WriteString(pad)
			writeJSON(b, it, depth+1)
			if i < len(v)-1 {
				b.WriteString(",")
			}
			b.WriteString("\n")
		}
		b.WriteString(strings.Repeat("  ", depth) + "]")
	default:
		enc, _ := json.Marshal(v)
		b.Write(enc)
	}
}

func studioDoc(root string) (string, error) {
	s, err := loadSpec(root)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(Header)
	info := get(s.root, "info")
	fmt.Fprintf(&b, "# Studio, admin and relay API\n\n%s\n\n", strings.TrimSpace(str(get(info, "description"))))
	b.WriteString("The same API as an OpenAPI 3.1 file: [studio.yaml](../openapi/studio.yaml). This page is generated from it.\n\n")

	b.WriteString("## Signing\n\n")
	for _, sc := range items(get(get(s.root, "components"), "securitySchemes")) {
		if str(get(sc.Value, "type")) == "http" {
			continue
		}
		fmt.Fprintf(&b, "**%s.** %s\n\n", code(str(sc.Key)), oneLine(str(get(sc.Value, "description"))))
	}
	var hrows [][]string
	for _, p := range items(get(get(s.root, "components"), "parameters")) {
		if name := str(get(p.Value, "name")); signingHeaders[name] {
			hrows = append(hrows, []string{code(name), oneLine(str(get(p.Value, "description")))})
		}
	}
	b.WriteString("Headers on every signed call:\n\n")
	b.WriteString(mdTable([]string{"Header", "Meaning"}, hrows))
	b.WriteString("\n")

	ops := s.Operations()
	for _, tag := range list(get(s.root, "tags")) {
		name := str(get(tag, "name"))
		fmt.Fprintf(&b, "## %s\n\n%s\n\n", name, oneLine(str(get(tag, "description"))))
		for _, op := range ops {
			if !hasTag(op.Node, name) {
				continue
			}
			renderOp(&b, s, op)
		}
	}

	b.WriteString("## Schemas\n\n")
	for _, sc := range items(get(get(s.root, "components"), "schemas")) {
		fmt.Fprintf(&b, "### %s\n\n", str(sc.Key))
		b.WriteString(propTable(s, sc.Value))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n", nil
}

func hasTag(op any, tag string) bool {
	for _, t := range list(get(op, "tags")) {
		if str(t) == tag {
			return true
		}
	}
	return false
}

func renderOp(b *strings.Builder, s *spec, op Operation) {
	fmt.Fprintf(b, "### `%s %s`\n\n**%s.**", op.Method, op.Path, strings.TrimSuffix(str(get(op.Node, "summary")), "."))
	if d := strings.TrimSpace(str(get(op.Node, "description"))); d != "" {
		b.WriteString(" " + oneLine(d))
	}
	b.WriteString("\n\n")

	switch {
	case len(list(get(op.Node, "security"))) == 0:
		b.WriteString("Auth: none.\n\n")
	default:
		sec := items(list(get(op.Node, "security"))[0])
		fmt.Fprintf(b, "Auth: %s.\n\n", code(str(sec[0].Key)))
	}

	var prows [][]string
	for _, p := range list(get(op.Node, "parameters")) {
		p = s.resolve(p)
		if signingHeaders[str(get(p, "name"))] {
			continue
		}
		req := "no"
		if v, ok := get(p, "required").(bool); ok && v {
			req = "yes"
		}
		prows = append(prows, []string{code(str(get(p, "name"))), str(get(p, "in")), schemaType(s, get(p, "schema")), req, oneLine(str(get(p, "description")))})
	}
	if len(prows) > 0 {
		b.WriteString(mdTable([]string{"Parameter", "In", "Type", "Required", "Meaning"}, prows))
		b.WriteString("\n")
	}

	if rb := get(op.Node, "requestBody"); rb != nil {
		var ct []string
		for _, c := range items(get(rb, "content")) {
			ct = append(ct, code(str(c.Key)))
		}
		fmt.Fprintf(b, "Request body (%s): %s\n\n", strings.Join(ct, ", "), oneLine(str(get(rb, "description"))))
		if first := items(get(rb, "content")); len(first) > 0 {
			if t := propTable(s, s.resolve(get(first[0].Value, "schema"))); t != "" {
				b.WriteString(t + "\n")
			}
		}
	}

	var rrows [][]string
	var example string
	for _, r := range items(get(op.Node, "responses")) {
		resp := s.resolve(r.Value)
		body := ""
		for _, c := range items(get(resp, "content")) {
			sch := get(c.Value, "schema")
			body = schemaType(s, sch)
			if ex := get(c.Value, "example"); ex != nil {
				if e := str(get(ex, "error")); e != "" {
					body += ", error " + code(e)
				} else if example == "" && strings.HasPrefix(str(r.Key), "2") {
					example = toJSON(ex)
				}
			}
			break
		}
		rrows = append(rrows, []string{code(str(r.Key)), oneLine(str(get(resp, "description"))), body})
	}
	b.WriteString(mdTable([]string{"Status", "Meaning", "Body"}, rrows))
	if example != "" {
		fmt.Fprintf(b, "\nExample response:\n\n```json\n%s\n```\n", example)
	}
	b.WriteString("\n")
}
