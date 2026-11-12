package audit

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCustomRuleUsesOnlyFixedSelectedFields(t *testing.T) {
	for _, name := range []string{"Customer defined rule", "Yolo UID set to root"} {
		raw, _ := json.Marshal(map[string]any{"time": "2026-11-12T01:00:00Z", "rule": name, "priority": "Warning", "output": "RAW_TEMPLATE_SECRET", "output_fields": map[string]any{"proc.exepath": "/usr/bin/example", "proc.name": "example", "user.uid": 1000, "evt.res": "SUCCESS", "proc.cmdline": "ARGV_SECRET", "proc.env": "ENV_SECRET", "evt.args": "AGGREGATE_SECRET", "unknown.field": "UNKNOWN_SECRET"}})
		event, err := ParseEvent(raw)
		if err != nil {
			t.Fatal(err)
		}
		rendered := RenderPrettyOutput(event)
		stored, _ := json.Marshal(event)
		for _, secret := range []string{"RAW_TEMPLATE_SECRET", "ARGV_SECRET", "ENV_SECRET", "AGGREGATE_SECRET", "UNKNOWN_SECRET"} {
			if strings.Contains(rendered, secret) || strings.Contains(string(stored), secret) {
				t.Fatalf("custom output leaked %s", secret)
			}
		}
		if name == "Customer defined rule" {
			if !strings.Contains(rendered, "Audit event") || !strings.Contains(rendered, "process=/usr/bin/example") || !strings.Contains(rendered, "uid=1000") || strings.Contains(rendered, "<NA>") {
				t.Fatal(rendered)
			}
		} else if strings.HasPrefix(rendered, "Audit event") {
			t.Fatal("built-in name lost its original template", rendered)
		}
	}
}
