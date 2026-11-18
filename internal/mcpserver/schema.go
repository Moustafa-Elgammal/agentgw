package mcpserver

import (
	"sort"

	"github.com/Moustafa-Elgammal/agentgw/internal/core"
)

// InputSchema compiles a tool's params into a JSON Schema object for MCP's
// inputSchema field. The schema is advice for the agent. The gateway's own
// validation (gateway.Validate) stays authoritative, because the SDK does not
// enforce raw schemas and invalid attempts must still count against the rate limit.
func InputSchema(t core.Tool) map[string]any {
	props := make(map[string]any, len(t.Params))
	required := []string{}
	for name, p := range t.Params {
		prop := map[string]any{"type": p.Type}
		if len(p.Enum) > 0 {
			prop["enum"] = p.Enum
		}
		if p.Min != nil {
			prop["minimum"] = *p.Min
		}
		if p.Max != nil {
			prop["maximum"] = *p.Max
		}
		props[name] = prop
		if !p.Optional {
			required = append(required, name)
		}
	}
	sort.Strings(required)
	schema := map[string]any{
		"type":                 "object",
		"properties":           props,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}
