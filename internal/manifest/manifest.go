// Package manifest loads and validates a studio's tool manifest.
//
// Two formats compile to the same structure: JSON (the wire format) and YAML
// (the authoring format). Both are strict: unknown fields are errors.
package manifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"

	"github.com/Moustafa-Elgammal/agentgw/internal/core"
)

// Manifest is the top-level document.
type Manifest struct {
	Version int         `json:"version"`
	Game    string      `json:"game,omitempty"`
	Tools   []core.Tool `json:"tools"`
}

var (
	nameRe      = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)
	validTypes  = map[string]bool{"string": true, "integer": true, "number": true, "boolean": true}
	validMethod = map[string]bool{"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true}
	placeholder = regexp.MustCompile(`\{([a-z][a-z0-9_]*)\}`)
)

// LoadFile reads and validates a manifest file. A .yaml or .yml extension selects
// the YAML format, anything else is read as JSON.
func LoadFile(path string) (*Manifest, error) {
	f, err := os.Open(path) //nolint:gosec // path comes from the operator's own CLI flag
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if ext := strings.ToLower(filepath.Ext(path)); ext == ".yaml" || ext == ".yml" {
		return ParseYAML(f)
	}
	return Parse(f)
}

// maxManifest bounds what ParseYAML will read.
const maxManifest = 4 << 20

// ParseYAML decodes and validates a YAML manifest. Unknown fields and duplicate
// keys are rejected so typos fail loudly, the same as for JSON.
func ParseYAML(r io.Reader) (*Manifest, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxManifest+1))
	if err != nil {
		return nil, fmt.Errorf("manifest: read: %w", err)
	}
	if len(data) > maxManifest {
		return nil, errors.New("manifest: file too large")
	}
	var m Manifest
	if err := yaml.UnmarshalWithOptions(data, &m, yaml.Strict()); err != nil {
		return nil, fmt.Errorf("manifest: decode yaml: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Parse decodes and validates a manifest. Unknown fields are rejected so typos fail loudly.
func Parse(r io.Reader) (*Manifest, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("manifest: decode: %w", err)
	}
	if dec.More() {
		return nil, errors.New("manifest: trailing data after document")
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Validate checks every rule and reports all problems at once.
func (m *Manifest) Validate() error {
	var problems []string
	add := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }

	if m.Version != 1 {
		add("version must be 1, got %d", m.Version)
	}
	if len(m.Tools) == 0 {
		add("at least one tool is required")
	}
	seen := map[string]bool{}
	for i, t := range m.Tools {
		where := fmt.Sprintf("tools[%d] (%q)", i, t.Name)
		if !nameRe.MatchString(t.Name) {
			add("%s: name must match %s", where, nameRe)
		}
		if seen[t.Name] {
			add("%s: duplicate tool name", where)
		}
		seen[t.Name] = true
		if strings.TrimSpace(t.Description) == "" {
			add("%s: description is required (the agent reads it)", where)
		}
		if !t.Tier.Valid() {
			add("%s: unknown tier %q", where, t.Tier)
		}
		if !validMethod[t.HTTP.Method] {
			add("%s: http.method must be one of GET, POST, PUT, PATCH, DELETE", where)
		}
		if !strings.HasPrefix(t.HTTP.URL, "/") {
			add("%s: http.url must be a path starting with /", where)
		}
		for _, ph := range placeholder.FindAllStringSubmatch(t.HTTP.URL, -1) {
			if _, ok := t.Params[ph[1]]; !ok {
				add("%s: url placeholder {%s} has no matching param", where, ph[1])
			}
		}
		if t.Tier == core.TierRead && (t.Caps.PerDayAmount > 0 || t.Caps.PerDayCalls > 0) {
			add("%s: read tools take no daily caps", where)
		}
		if t.Caps.PerDayAmount > 0 {
			p, ok := t.Params[t.Caps.AmountParam]
			if !ok || p.Type != "integer" {
				add("%s: caps.per_day_amount needs caps.amount_param naming an integer param", where)
			}
		}
		if t.Caps.PerDayAmount < 0 || t.Caps.PerDayCalls < 0 || t.Caps.CooldownSeconds < 0 {
			add("%s: caps must not be negative", where)
		}
		names := make([]string, 0, len(t.Params))
		for n := range t.Params {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			p := t.Params[n]
			if !validTypes[p.Type] {
				add("%s: param %q has unknown type %q", where, n, p.Type)
			}
			if len(p.Enum) > 0 && p.Type != "string" {
				add("%s: param %q: enum only applies to strings", where, n)
			}
			if (p.Min != nil || p.Max != nil) && p.Type != "integer" && p.Type != "number" {
				add("%s: param %q: min/max only apply to numbers", where, n)
			}
			if p.Min != nil && p.Max != nil && *p.Min > *p.Max {
				add("%s: param %q: min is greater than max", where, n)
			}
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("manifest invalid:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

// Marshal renders the manifest as indented JSON (used by tooling and tests).
func (m *Manifest) Marshal() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
