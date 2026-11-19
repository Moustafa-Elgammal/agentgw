package connector_test

import (
	"context"
	"testing"

	"github.com/Moustafa-Elgammal/agentgw/internal/connector"
	"github.com/Moustafa-Elgammal/agentgw/internal/core"
)

type tag string

func (g tag) Call(context.Context, core.Principal, core.Tool, map[string]any, string) (core.Result, error) {
	return core.Result{OK: true, Code: string(g)}, nil
}

func TestMux(t *testing.T) {
	tests := []struct {
		name    string
		mux     connector.Mux
		backend string
		want    string
		wantErr bool
	}{
		{"default is http", connector.Mux{HTTP: tag("h"), Relay: tag("r")}, "", "h", false},
		{"explicit http", connector.Mux{HTTP: tag("h"), Relay: tag("r")}, core.BackendHTTP, "h", false},
		{"relay", connector.Mux{HTTP: tag("h"), Relay: tag("r")}, core.BackendRelay, "r", false},
		{"relay not configured", connector.Mux{HTTP: tag("h")}, core.BackendRelay, "", true},
		{"unknown backend", connector.Mux{HTTP: tag("h"), Relay: tag("r")}, "ftp", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := tt.mux.Call(context.Background(), core.Principal{}, core.Tool{Name: "x", Backend: tt.backend}, nil, "")
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if res.Code != tt.want {
				t.Fatalf("routed to %q, want %q", res.Code, tt.want)
			}
		})
	}
}
