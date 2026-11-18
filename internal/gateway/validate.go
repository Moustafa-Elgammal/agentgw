package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/Moustafa-Elgammal/agentgw/internal/core"
)

func nowUTC() time.Time { return time.Now().UTC() }

// Validate decodes raw arguments, checks them against the tool's params and
// returns the typed map plus the amount to claim against caps.AmountParam.
// Error messages are written for the agent: they name the field and the fix.
func Validate(t core.Tool, raw json.RawMessage) (map[string]any, int64, error) {
	args := map[string]any{}
	if len(bytes.TrimSpace(raw)) > 0 && string(bytes.TrimSpace(raw)) != "null" {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&args); err != nil {
			return nil, 0, fmt.Errorf("arguments must be a JSON object: %v", err)
		}
	}
	names := make([]string, 0, len(t.Params))
	for n := range t.Params {
		names = append(names, n)
	}
	sort.Strings(names)

	for k := range args {
		if _, ok := t.Params[k]; !ok {
			return nil, 0, fmt.Errorf("unknown argument %q. Valid arguments: %v", k, names)
		}
	}
	out := make(map[string]any, len(args))
	for _, n := range names {
		p := t.Params[n]
		v, present := args[n]
		if !present || v == nil {
			if p.Optional {
				continue
			}
			return nil, 0, fmt.Errorf("missing argument %q (%s)", n, p.Type)
		}
		cv, err := convert(n, p, v)
		if err != nil {
			return nil, 0, err
		}
		out[n] = cv
	}

	var amount int64
	if t.Caps.AmountParam != "" {
		if v, ok := out[t.Caps.AmountParam].(int64); ok {
			amount = v
		}
	}
	return out, amount, nil
}

func convert(name string, p core.Param, v any) (any, error) {
	switch p.Type {
	case "string":
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("argument %q must be a string", name)
		}
		if len(p.Enum) > 0 && !slices.Contains(p.Enum, s) {
			return nil, fmt.Errorf("invalid %s %q: choose one of %v", name, s, p.Enum)
		}
		return s, nil
	case "boolean":
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("argument %q must be true or false", name)
		}
		return b, nil
	case "integer":
		n, ok := v.(json.Number)
		if !ok {
			return nil, fmt.Errorf("argument %q must be an integer", name)
		}
		i, err := n.Int64()
		if err != nil {
			return nil, fmt.Errorf("argument %q must be an integer", name)
		}
		if err := checkRange(name, p, float64(i)); err != nil {
			return nil, err
		}
		return i, nil
	case "number":
		n, ok := v.(json.Number)
		if !ok {
			return nil, fmt.Errorf("argument %q must be a number", name)
		}
		f, err := n.Float64()
		if err != nil {
			return nil, fmt.Errorf("argument %q must be a number", name)
		}
		if err := checkRange(name, p, f); err != nil {
			return nil, err
		}
		return f, nil
	}
	return nil, fmt.Errorf("argument %q has unsupported type %q", name, p.Type)
}

func checkRange(name string, p core.Param, f float64) error {
	if p.Min != nil && f < float64(*p.Min) {
		return fmt.Errorf("argument %q must be at least %d", name, *p.Min)
	}
	if p.Max != nil && f > float64(*p.Max) {
		return fmt.Errorf("argument %q must be at most %d", name, *p.Max)
	}
	return nil
}
