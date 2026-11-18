package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Moustafa-Elgammal/agentgw/internal/policy"
)

func TestStructuredContentMatchesText(t *testing.T) {
	tests := []struct {
		name     string
		tool     string
		args     map[string]any
		wantErr  bool
		wantCode string
		wantData bool
	}{
		{"success with data", "get_state", nil, false, "", true},
		{"denial with code", "sell_resource", map[string]any{"resource": "gold", "quantity": 1}, true, "invalid_arguments", false},
		{"cap denial", "sell_resource", map[string]any{"resource": "food", "quantity": 101}, true, "daily_amount_cap", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := setup(t, policy.DefaultLimits)
			res, err := connect(t, e.url, "p1").CallTool(context.Background(), &mcp.CallToolParams{Name: tc.tool, Arguments: tc.args})
			if err != nil {
				t.Fatal(err)
			}
			if res.IsError != tc.wantErr {
				t.Fatalf("isError = %v", res.IsError)
			}
			var fromText map[string]any
			if err := json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &fromText); err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(res.StructuredContent)
			if err != nil {
				t.Fatal(err)
			}
			var fromStructured map[string]any
			if err := json.Unmarshal(raw, &fromStructured); err != nil {
				t.Fatalf("structuredContent is not an object: %s", raw)
			}
			a, _ := json.Marshal(fromText)
			b, _ := json.Marshal(fromStructured)
			if string(a) != string(b) {
				t.Errorf("text %s != structured %s", a, b)
			}
			if fromStructured["ok"] != !tc.wantErr || (tc.wantCode != "" && fromStructured["code"] != tc.wantCode) {
				t.Errorf("unexpected body %s", b)
			}
			if _, has := fromStructured["data"]; has != tc.wantData {
				t.Errorf("data present = %v, want %v", has, tc.wantData)
			}
		})
	}
}

func TestToolsDeclarePermissiveOutputSchema(t *testing.T) {
	e := setup(t, policy.DefaultLimits)
	res, err := connect(t, e.url, "p1").ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range res.Tools {
		raw, _ := json.Marshal(tl.OutputSchema)
		var sc struct {
			Type     string   `json:"type"`
			Required []string `json:"required"`
		}
		if err := json.Unmarshal(raw, &sc); err != nil || sc.Type != "object" || len(sc.Required) != 1 || sc.Required[0] != "ok" {
			t.Errorf("%s: output schema must be an object requiring only ok, got %s", tl.Name, raw)
		}
	}
}
