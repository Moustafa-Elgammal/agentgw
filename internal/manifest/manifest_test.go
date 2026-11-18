package manifest

import (
	"strings"
	"testing"
)

const good = `{
  "version": 1,
  "game": "city-demo",
  "tools": [
    {"name": "get_city_state", "description": "Overview of the city.", "tier": "read",
     "http": {"method": "GET", "url": "/api/v1/city"}},
    {"name": "sell_resource", "description": "Sell a resource at the current price.", "tier": "medium",
     "caps": {"per_day_amount": 300, "amount_param": "quantity"},
     "http": {"method": "POST", "url": "/api/v1/market/sell"},
     "params": {"resource": {"type": "string", "enum": ["food", "water"]},
                "quantity": {"type": "integer", "min": 1, "max": 1000}}}
  ]
}`

func TestParseGood(t *testing.T) {
	m, err := Parse(strings.NewReader(good))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Tools) != 2 || m.Tools[1].Caps.PerDayAmount != 300 {
		t.Fatalf("unexpected manifest: %+v", m)
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		"unknown field":       `{"version":1,"tools":[{"name":"ab","description":"d","tier":"read","http":{"method":"GET","url":"/x"},"oops":1}]}`,
		"bad version":         `{"version":2,"tools":[{"name":"ab","description":"d","tier":"read","http":{"method":"GET","url":"/x"}}]}`,
		"no tools":            `{"version":1,"tools":[]}`,
		"bad tier":            `{"version":1,"tools":[{"name":"ab","description":"d","tier":"root","http":{"method":"GET","url":"/x"}}]}`,
		"missing description": `{"version":1,"tools":[{"name":"ab","description":" ","tier":"read","http":{"method":"GET","url":"/x"}}]}`,
		"relative url":        `{"version":1,"tools":[{"name":"ab","description":"d","tier":"read","http":{"method":"GET","url":"x"}}]}`,
		"placeholder":         `{"version":1,"tools":[{"name":"ab","description":"d","tier":"read","http":{"method":"GET","url":"/x/{id}"}}]}`,
		"cap without param":   `{"version":1,"tools":[{"name":"ab","description":"d","tier":"medium","caps":{"per_day_amount":5},"http":{"method":"POST","url":"/x"}}]}`,
		"read with caps":      `{"version":1,"tools":[{"name":"ab","description":"d","tier":"read","caps":{"per_day_calls":5},"http":{"method":"GET","url":"/x"}}]}`,
		"read with confirm":   `{"version":1,"tools":[{"name":"ab","description":"d","tier":"read","confirm":true,"http":{"method":"GET","url":"/x"}}]}`,
		"duplicate":           `{"version":1,"tools":[{"name":"ab","description":"d","tier":"read","http":{"method":"GET","url":"/x"}},{"name":"ab","description":"d","tier":"read","http":{"method":"GET","url":"/y"}}]}`,
		"trailing":            good + ` {}`,
	}
	for name, doc := range cases {
		if _, err := Parse(strings.NewReader(doc)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestConfirmFlagParses(t *testing.T) {
	m, err := Parse(strings.NewReader(`{"version":1,"tools":[{"name":"ab","description":"d","tier":"medium","confirm":true,"http":{"method":"POST","url":"/x"}}]}`))
	if err != nil || !m.Tools[0].Confirm {
		t.Fatalf("confirm must parse on a non-read tool: %v %+v", err, m)
	}
}
