package docgen

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/Moustafa-Elgammal/agentgw/internal/audit"
)

var auditText = map[string]fieldDoc{
	"id":          {"feed only", "Increasing id that orders events. Also the paging cursor of the activity feed. Not part of the event itself."},
	"time":        {"yes", "When the attempt was recorded (UTC)."},
	"tenant":      {"yes", "The tenant the call belongs to. Not shown in the player feed."},
	"player":      {"yes", "The player the call was for. The admin feed shows it, the player feed leaves it out."},
	"tool":        {"yes", "The tool name the agent called. `unknown_tool` attempts record the name that was asked for. Kill switch changes made through the admin API record `admin.switch.on` or `admin.switch.off`."},
	"tier":        {"yes", "The tool's tier, empty when the tool is unknown. `admin` for kill switch changes."},
	"outcome":     {"yes", "`ok`, `denied` (a policy or validation refusal) or `error` (infrastructure trouble)."},
	"code":        {"when not ok", "The result code of a denial or error, see [Result codes](errors.md)."},
	"args_sha256": {"yes", "Hex SHA-256 of the validated arguments with keys sorted. The arguments themselves are never stored."},
}

func auditDoc() (string, error) {
	var b strings.Builder
	b.WriteString(Header)
	b.WriteString("# Audit events\n\n")
	b.WriteString("Every tool call attempt, valid or not, produces one audit event. The gateway logs it as structured JSON and keeps it for the activity feed (memory, or Postgres with `--postgres-url`) for `--audit-retention`. This page is generated from the event struct. The audit log stores a hash of the arguments, never the arguments or any game data.\n\n")
	fields := jsonFields(reflect.TypeOf(audit.Stored{}))
	rows := make([][]string, 0, len(fields))
	seen := 0
	for _, f := range fields {
		d, ok := auditText[f.Name]
		if !ok {
			return "", fmt.Errorf("audit field %q has no description in docgen/audit.go", f.Name)
		}
		seen++
		rows = append(rows, []string{code(f.Name), f.Type, d.Required, d.Text})
	}
	if seen != len(auditText) {
		return "", fmt.Errorf("docgen/audit.go describes fields that audit.Event no longer has")
	}
	b.WriteString(mdTable([]string{"Field", "Type", "Present", "Meaning"}, rows))
	b.WriteString("\n## Outcomes\n\n")
	b.WriteString(mdTable([]string{"Outcome", "Meaning"}, [][]string{
		{"`ok`", "The call ran and the game answered."},
		{"`denied`", "Refused before or by the game: policy, validation, scope, confirmation, or a backend 4xx."},
		{"`error`", "The gateway could not finish: the game was unreachable, a store failed, or the outcome is unknown."},
	}))
	b.WriteString("\nThe feed endpoints return these events, see the [studio API](studio-api.md).\n")
	return b.String(), nil
}
