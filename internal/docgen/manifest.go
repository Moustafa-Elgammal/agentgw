package docgen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/Moustafa-Elgammal/agentgw/internal/core"
	"github.com/Moustafa-Elgammal/agentgw/internal/gateway"
	"github.com/Moustafa-Elgammal/agentgw/internal/manifest"
)

// fieldDoc is the human text for one manifest field. The generator takes names and types from
// the Go structs and the text from here; a test fails when a struct field has no entry.
type fieldDoc struct {
	Required string
	Text     string
}

type structDoc struct {
	Title  string
	Intro  string
	Type   reflect.Type
	Fields map[string]fieldDoc
}

var tierText = map[core.Tier]string{
	core.TierRead:      "State, prices, advice. Rate limit only. Allowed for coach and operator grants.",
	core.TierSafe:      "Same rules as a human player. Cooldowns and rate limit apply.",
	core.TierMedium:    "Spends in-game currency. Daily caps apply.",
	core.TierSensitive: "Paid currency, gifts, deletion. Disabled: every call is refused with `tier_disabled`.",
}

var backendText = map[string]string{
	core.BackendHTTP:  "Signed REST call to the studio's backend (levels 0 and 1). The default.",
	core.BackendRelay: "Call sent over the player's game client WebSocket (see the relay section of the protocol page). Takes no `http` binding.",
}

func manifestStructs() []structDoc {
	return []structDoc{
		{"Manifest", "The top-level document. JSON or YAML. Unknown fields and duplicate keys are rejected.", reflect.TypeOf(manifest.Manifest{}), map[string]fieldDoc{
			"version": {"yes", "Format version. Must be `1`."},
			"game":    {"no", "A label for the game, shown in logs."},
			"tools":   {"yes", "The tools the agent may call. At least one."},
		}},
		{"Tool", "One capability a player's agent may call.", reflect.TypeOf(core.Tool{}), map[string]fieldDoc{
			"name":        {"yes", "Tool name, unique in the manifest. Must match the name pattern below."},
			"description": {"yes", "What the tool does, written for the agent. Must not be blank."},
			"tier":        {"yes", "Risk class. Decides which policy controls apply. See tiers below."},
			"caps":        {"no", "Per-tool limits."},
			"confirm":     {"no", "When true, every call needs the player's approval first. Not allowed on `read` tools."},
			"backend":     {"no", "How the call reaches the game: `http` (default) or `relay`."},
			"http":        {"http backend only", "Maps the tool onto a backend REST endpoint. Required for `http` tools, must be absent (or empty) for `relay` tools."},
			"params":      {"no", "The tool's arguments, by name. Calls with other arguments are refused."},
		}},
		{"caps", "Optional per-tool limits. Zero or absent means not set. Read tools take no daily caps.", reflect.TypeOf(core.Caps{}), map[string]fieldDoc{
			"per_day_calls":    {"no", "Most successful calls per player per day."},
			"per_day_amount":   {"no", "Most units per player per day. Needs `amount_param`."},
			"amount_param":     {"with per_day_amount", "Name of an integer param whose value is added to the daily amount."},
			"cooldown_seconds": {"no", "Seconds a player must wait after a successful call."},
		}},
		{"http", "Maps a tool onto a REST endpoint.", reflect.TypeOf(core.HTTPBinding{}), map[string]fieldDoc{
			"method": {"yes", "HTTP method."},
			"url":    {"yes", "A path starting with `/`. `{name}` placeholders take their value from the param of the same name."},
		}},
		{"params.<name>", "One tool argument. The key in `params` is the argument name.", reflect.TypeOf(core.Param{}), map[string]fieldDoc{
			"type":     {"yes", "Argument type."},
			"enum":     {"no", "Allowed values. Strings only. Matching is exact."},
			"min":      {"no", "Smallest allowed value. Numbers only."},
			"max":      {"no", "Largest allowed value. Numbers only."},
			"optional": {"no", "When true the argument may be left out."},
		}},
	}
}

func manifestDoc() (string, error) {
	var b strings.Builder
	b.WriteString(Header)
	b.WriteString("# Manifest reference\n\n")
	b.WriteString("The manifest lists the tools a game exposes to a player's agent. This page is generated from the validator, so it matches the code. The format is described in prose in [Manifest](../MANIFEST.md), and a machine readable [JSON Schema](manifest.schema.json) is generated next to this page.\n\n")
	b.WriteString("Check a file with `gwctl validate manifest.yaml`. The validator reports every problem at once.\n\n")
	for _, s := range manifestStructs() {
		fields := jsonFields(s.Type)
		rows := make([][]string, 0, len(fields))
		for _, f := range fields {
			d, ok := s.Fields[f.Name]
			if !ok {
				return "", fmt.Errorf("manifest field %s.%s has no description in docgen/manifest.go", s.Title, f.Name)
			}
			rows = append(rows, []string{code(f.Name), f.Type, d.Required, d.Text})
		}
		if len(rows) != len(s.Fields) {
			return "", fmt.Errorf("docgen/manifest.go describes fields that %s no longer has", s.Title)
		}
		fmt.Fprintf(&b, "## %s\n\n%s\n\n", s.Title, s.Intro)
		b.WriteString(mdTable([]string{"Field", "Type", "Required", "Meaning"}, rows))
		b.WriteString("\n")
	}
	b.WriteString("## Tiers\n\n")
	var tiers [][]string
	for _, t := range core.Tiers {
		txt, ok := tierText[t]
		if !ok {
			return "", fmt.Errorf("tier %q has no description in docgen/manifest.go", t)
		}
		tiers = append(tiers, []string{code(string(t)), txt})
	}
	b.WriteString(mdTable([]string{"Tier", "Meaning"}, tiers))
	b.WriteString("\n## Backends\n\n")
	var backends [][]string
	for _, n := range []string{core.BackendHTTP, core.BackendRelay} {
		backends = append(backends, []string{code(n), backendText[n]})
	}
	b.WriteString(mdTable([]string{"Backend", "Meaning"}, backends))

	b.WriteString("\n## Rules\n\n")
	fmt.Fprintf(&b, "- Tool names match `%s` (lowercase, digits and underscores, 2 to 64 characters, starting with a letter) and are unique.\n", manifest.NamePattern())
	fmt.Fprintf(&b, "- `http.method` is one of %s.\n", codes(manifest.HTTPMethods()))
	b.WriteString("- `http.url` starts with `/`. Every `{name}` placeholder needs a param of that name. Placeholder values that are empty, `.` or `..` are refused at call time.\n")
	fmt.Fprintf(&b, "- Param `type` is one of %s. `enum` applies to `string` params only. `min` and `max` apply to `integer` and `number` params only, and `min` must not exceed `max`.\n", codes(manifest.ParamTypes()))
	b.WriteString("- `caps.per_day_amount` needs `caps.amount_param` naming an `integer` param.\n")
	b.WriteString("- `caps` values must not be negative.\n")
	b.WriteString("- `read` tools cannot set `confirm` and take no `per_day_amount` or `per_day_calls`.\n")
	b.WriteString("- `relay` tools take no `http` binding.\n")
	fmt.Fprintf(&b, "- A YAML manifest is read up to %d MiB. The studio upload endpoint accepts up to 1 MiB.\n", manifest.MaxYAMLBytes>>20)
	fmt.Fprintf(&b, "- At call time a string argument is at most %d bytes.\n", gateway.MaxStringArg)
	m, err := manifest.ParseYAML(strings.NewReader(exampleManifest))
	if err != nil {
		return "", fmt.Errorf("docgen example manifest: %w", err)
	}
	raw, err := m.Marshal()
	if err != nil {
		return "", err
	}
	var asJSON bytes.Buffer
	if err := json.Indent(&asJSON, raw, "", "  "); err != nil {
		return "", err
	}
	b.WriteString("\n## Example\n\nYAML is the recommended authoring format:\n\n```yaml\n")
	b.WriteString(exampleManifest)
	b.WriteString("```\n\nThe same manifest as JSON, which is what the gateway stores and what `PUT /v1/studio/manifest` returns:\n\n```json\n")
	b.Write(asJSON.Bytes())
	b.WriteString("\n```\n")
	return b.String(), nil
}

const exampleManifest = `version: 1
game: city-demo
tools:
  - name: get_city_state
    description: Overview of the city.
    tier: read
    http: {method: GET, url: /api/v1/city}

  - name: sell_resource
    description: Sell a resource at the current price.
    tier: medium
    caps: {per_day_amount: 300, amount_param: quantity}
    http: {method: POST, url: /api/v1/market/sell}
    params:
      resource: {type: string, enum: [food, water]}
      quantity: {type: integer, min: 1, max: 1000}
`

// ManifestSchema returns the JSON Schema (2020-12) of the manifest, as indented JSON.
// Rules a schema cannot express (placeholders naming params, amount_param naming an integer
// param, min not above max, unique names) stay in the validator and are listed in manifest.md.
func ManifestSchema() ([]byte, error) {
	type m = map[string]any
	tiers := make([]string, len(core.Tiers))
	for i, t := range core.Tiers {
		tiers[i] = string(t)
	}
	schema := m{
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
		"$id":                  "https://github.com/Moustafa-Elgammal/agentgw/docs/reference/manifest.schema.json",
		"title":                "agentgw manifest",
		"description":          "Generated by make docs-gen from the manifest validator. Do not edit.",
		"type":                 "object",
		"required":             []string{"version", "tools"},
		"additionalProperties": false,
		"properties": m{
			"version": m{"const": 1},
			"game":    m{"type": "string"},
			"tools":   m{"type": "array", "minItems": 1, "items": m{"$ref": "#/$defs/tool"}},
		},
		"$defs": m{
			"tool": m{
				"type":                 "object",
				"required":             []string{"name", "description", "tier"},
				"additionalProperties": false,
				"properties": m{
					"name":        m{"type": "string", "pattern": manifest.NamePattern()},
					"description": m{"type": "string", "pattern": `\S`},
					"tier":        m{"enum": tiers},
					"caps":        m{"$ref": "#/$defs/caps"},
					"confirm":     m{"type": "boolean"},
					"backend":     m{"enum": []string{core.BackendHTTP, core.BackendRelay}},
					"http":        m{"$ref": "#/$defs/http"},
					"params":      m{"type": "object", "additionalProperties": m{"$ref": "#/$defs/param"}},
				},
				"allOf": []m{
					{
						"if":   m{"required": []string{"backend"}, "properties": m{"backend": m{"const": core.BackendRelay}}},
						"then": m{"properties": m{"http": m{"properties": m{"method": m{"maxLength": 0}, "url": m{"maxLength": 0}}}}},
						"else": m{"required": []string{"http"}, "properties": m{"http": m{
							"required":   []string{"method", "url"},
							"properties": m{"method": m{"minLength": 1}, "url": m{"minLength": 1}},
						}}},
					},
					{
						"if": m{"required": []string{"tier"}, "properties": m{"tier": m{"const": string(core.TierRead)}}},
						"then": m{"properties": m{
							"confirm": m{"const": false},
							"caps":    m{"properties": m{"per_day_amount": m{"maximum": 0}, "per_day_calls": m{"maximum": 0}}},
						}},
					},
				},
			},
			"caps": m{
				"type":                 "object",
				"additionalProperties": false,
				"properties": m{
					"per_day_calls":    m{"type": "integer", "minimum": 0},
					"per_day_amount":   m{"type": "integer", "minimum": 0},
					"amount_param":     m{"type": "string"},
					"cooldown_seconds": m{"type": "integer", "minimum": 0},
				},
				"if":   m{"required": []string{"per_day_amount"}, "properties": m{"per_day_amount": m{"minimum": 1}}},
				"then": m{"required": []string{"amount_param"}},
			},
			"http": m{
				"type":                 "object",
				"additionalProperties": false,
				"properties": m{
					"method": m{"type": "string", "enum": append(manifest.HTTPMethods(), "")},
					"url":    m{"type": "string", "pattern": `^(/.*)?$`},
				},
			},
			"param": m{
				"type":                 "object",
				"required":             []string{"type"},
				"additionalProperties": false,
				"properties": m{
					"type":     m{"enum": manifest.ParamTypes()},
					"enum":     m{"type": "array", "items": m{"type": "string"}},
					"min":      m{"type": "integer"},
					"max":      m{"type": "integer"},
					"optional": m{"type": "boolean"},
				},
				"allOf": []m{
					{"if": m{"required": []string{"enum"}}, "then": m{"properties": m{"type": m{"const": "string"}}}},
					{
						"if":   m{"anyOf": []m{{"required": []string{"min"}}, {"required": []string{"max"}}}},
						"then": m{"properties": m{"type": m{"enum": []string{"integer", "number"}}}},
					},
				},
			},
		},
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(schema); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
