package mcpserver

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Moustafa-Elgammal/agentgw/internal/core"
)

func i64(v int64) *int64 { return &v }

func TestInputSchema(t *testing.T) {
	tests := []struct {
		name string
		tool core.Tool
		want string
	}{
		{"no params", core.Tool{Name: "get_state"},
			`{"type":"object","properties":{},"additionalProperties":false}`},
		{"enum range required", core.Tool{Name: "sell", Params: map[string]core.Param{
			"resource": {Type: "string", Enum: []string{"food", "water"}},
			"quantity": {Type: "integer", Min: i64(1), Max: i64(1000)},
		}},
			`{"type":"object","properties":{"quantity":{"type":"integer","minimum":1,"maximum":1000},"resource":{"type":"string","enum":["food","water"]}},"required":["quantity","resource"],"additionalProperties":false}`},
		{"optional not required", core.Tool{Name: "x", Params: map[string]core.Param{
			"note": {Type: "string", Optional: true},
		}},
			`{"type":"object","properties":{"note":{"type":"string"}},"additionalProperties":false}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(InputSchema(tc.tool))
			if err != nil {
				t.Fatal(err)
			}
			var g, w any
			if err := json.Unmarshal(got, &g); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tc.want), &w); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(g, w) {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}
