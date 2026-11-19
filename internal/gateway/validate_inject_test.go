package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Moustafa-Elgammal/agentgw/internal/core"
)

func ptr(v int64) *int64 { return &v }

var injTool = core.Tool{Name: "t", Params: map[string]core.Param{
	"kind":  {Type: "string", Enum: []string{"food", "water"}},
	"qty":   {Type: "integer", Min: ptr(1), Max: ptr(1000)},
	"big":   {Type: "integer", Max: ptr(1 << 53), Optional: true},
	"price": {Type: "number", Min: ptr(0), Max: ptr(100)},
	"note":  {Type: "string", Optional: true},
	"flag":  {Type: "boolean", Optional: true},
}}

func TestValidateRejects(t *testing.T) {
	huge := strings.Repeat("a", MaxStringArg+1)
	deep := strings.Repeat(`{"a":`, 5000) + `1` + strings.Repeat(`}`, 5000)
	tests := []struct {
		name string
		raw  string
	}{
		{"array instead of object", `[1,2]`},
		{"string instead of object", `"kind"`},
		{"number instead of object", `5`},
		{"unterminated", `{"kind":"food"`},
		{"trailing second object", `{"kind":"food","qty":1,"price":1} {"qty":999}`},
		{"trailing garbage", `{"kind":"food","qty":1,"price":1} x`},
		{"enum wrong case", `{"kind":"Food","qty":1,"price":1}`},
		{"enum trailing space", `{"kind":"food ","qty":1,"price":1}`},
		{"enum leading newline", `{"kind":"\nfood","qty":1,"price":1}`},
		{"enum null byte", `{"kind":"food\u0000","qty":1,"price":1}`},
		{"enum unicode lookalike", `{"kind":"fo\u043od","qty":1,"price":1}`},
		{"integer as float", `{"kind":"food","qty":5.0,"price":1}`},
		{"integer exponent", `{"kind":"food","qty":1e2,"price":1}`},
		{"integer past int64", `{"kind":"food","qty":9223372036854775808,"price":1}`},
		{"integer far past int64", `{"kind":"food","qty":1e400,"price":1}`},
		{"integer as string", `{"kind":"food","qty":"5","price":1}`},
		{"integer NaN string", `{"kind":"food","qty":"NaN","price":1}`},
		{"number NaN string", `{"kind":"food","qty":1,"price":"NaN"}`},
		{"number Infinity string", `{"kind":"food","qty":1,"price":"Infinity"}`},
		{"number out of float range", `{"kind":"food","qty":1,"price":1e999}`},
		{"number over max", `{"kind":"food","qty":1,"price":100.0000001}`},
		{"big just over 2^53", `{"kind":"food","qty":1,"price":1,"big":9007199254740993}`},
		{"qty zero", `{"kind":"food","qty":0,"price":1}`},
		{"qty negative", `{"kind":"food","qty":-1,"price":1}`},
		{"unknown argument", `{"kind":"food","qty":1,"price":1,"admin":true}`},
		{"proto style key", `{"kind":"food","qty":1,"price":1,"__proto__":{}}`},
		{"nested object for string", `{"kind":{"a":"food"},"qty":1,"price":1}`},
		{"array for integer", `{"kind":"food","qty":[1],"price":1}`},
		{"boolean as string", `{"kind":"food","qty":1,"price":1,"flag":"true"}`},
		{"huge string", `{"kind":"food","qty":1,"price":1,"note":"` + huge + `"}`},
		{"deeply nested", deep},
		{"deeply nested in a param", `{"kind":"food","qty":1,"price":1,"note":` + strings.Repeat("[", 20000) + `}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := Validate(injTool, json.RawMessage(tt.raw)); err == nil {
				t.Fatalf("accepted %.80s", tt.raw)
			}
		})
	}
}

func TestValidateAccepts(t *testing.T) {
	tests := []struct {
		name, raw string
		qty       int64
	}{
		{"plain", `{"kind":"food","qty":7,"price":1.5}`, 7},
		{"whitespace around", " \n{\"kind\":\"food\",\"qty\":7,\"price\":1}\n ", 7},
		{"exact 2^53", `{"kind":"water","qty":1,"price":1,"big":9007199254740992}`, 1},
		{"max length string", `{"kind":"food","qty":2,"price":1,"note":"` + strings.Repeat("a", MaxStringArg) + `"}`, 2},
		{"odd characters pass through as data", `{"kind":"food","qty":3,"price":1,"note":"../../etc?a=b&c#\r\n\u0000"}`, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, _, err := Validate(injTool, json.RawMessage(tt.raw))
			if err != nil {
				t.Fatal(err)
			}
			if out["qty"] != tt.qty {
				t.Fatalf("qty = %v", out["qty"])
			}
		})
	}
}

// Duplicate keys: the last one wins, as in encoding/json. What is validated is exactly what is
// forwarded, so a duplicate cannot show the validator one value and the backend another.
func TestValidateDuplicateKeysLastWins(t *testing.T) {
	out, _, err := Validate(injTool, json.RawMessage(`{"kind":"food","qty":1,"qty":5,"price":1}`))
	if err != nil || out["qty"] != int64(5) {
		t.Fatalf("out=%v err=%v", out, err)
	}
	// An out of range duplicate hiding behind a valid one is still caught.
	if _, _, err := Validate(injTool, json.RawMessage(`{"kind":"food","qty":5,"qty":5000,"price":1}`)); err == nil {
		t.Fatal("range check missed the last duplicate")
	}
}

func TestNaNNeverReachesTheBackend(t *testing.T) {
	// JSON cannot carry NaN or Inf numbers; the decoder refuses the literals outright.
	for _, lit := range []string{"NaN", "Infinity", "-Infinity"} {
		raw := `{"kind":"food","qty":1,"price":` + lit + `}`
		if _, _, err := Validate(injTool, json.RawMessage(raw)); err == nil {
			t.Errorf("%s accepted", lit)
		}
	}
}
