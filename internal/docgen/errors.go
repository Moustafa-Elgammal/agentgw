package docgen

import (
	"strings"

	"github.com/Moustafa-Elgammal/agentgw/internal/core"
)

func errorsDoc() string {
	var b strings.Builder
	b.WriteString(Header)
	b.WriteString("# Result codes and errors\n\n")
	b.WriteString("This page is generated from the code registry (`core.Codes`). A test fails if the source emits a code that is not listed.\n\n")
	b.WriteString("## Result codes\n\n")
	b.WriteString("Expected failures of a tool call are normal results, not protocol errors: `ok` is false and `code` says why, with a `message` written for the agent. Over MCP the result has `isError` set and carries the same `code` and `message`. The codes are stable, so an agent can branch on them.\n\n")
	b.WriteString("```json\n{\"ok\":false,\"code\":\"daily_amount_cap\",\"message\":\"sell_resource: 80 needed, 20 left today\"}\n```\n\n")
	rows := make([][]string, 0, len(core.Codes))
	for _, c := range core.Codes {
		rows = append(rows, []string{code(c.Code), c.Source, c.Meaning, c.Action})
	}
	b.WriteString(mdTable([]string{"Code", "Emitted by", "Meaning", "What to do"}, rows))
	b.WriteString("\nEvery attempt is audited with its code, see [Audit events](audit-events.md).\n\n")
	b.WriteString("## HTTP errors of the studio, admin and relay endpoints\n\n")
	b.WriteString("The signed studio, admin and relay endpoints answer errors as `{\"error\":\"<code>\"}` with an HTTP status. The OAuth endpoints also add `error_description`. A bad signature, a replayed request and an unknown key all give the same `invalid_client`, so the response does not help an attacker.\n\n")
	hrows := make([][]string, 0, len(core.HTTPErrors))
	for _, e := range core.HTTPErrors {
		hrows = append(hrows, []string{code(e.Code), e.Status, e.Meaning})
	}
	b.WriteString(mdTable([]string{"Error", "Status", "Meaning"}, hrows))
	return b.String()
}
