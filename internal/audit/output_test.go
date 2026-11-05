package audit

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRenderedRuleRetainsSelectedFieldsWithoutRawOutput(t *testing.T) {
	fields := map[string]any{
		"proc.name": "yolo-audit-test", "proc.exepath": "/tmp/yolo-audit-test", "user.name": "root", "user.uid": 0,
		"container.id": "host", "container.name": "<NA>", "evt.res": "SUCCESS",
		"fs.path.name": "/etc/one", "fs.path.target": "/etc/two", "fd.name": "/run/socket",
		"yolo.box": "stale", "yolo.metadata_available": false,
		"proc.args": "SECRET_ARGV", "proc.env": "SECRET_ENV", "evt.buffer": "SECRET_BUFFER",
	}
	raw, _ := json.Marshal(map[string]any{"time": "2026-11-05T14:40:01Z", "rule": "Yolo audit test", "priority": "Notice", "output": "SECRET_RAW_OUTPUT", "output_fields": fields})
	event, err := ParseEvent(raw)
	if err != nil {
		t.Fatal(err)
	}
	if event.MetadataAvailable || event.Box != "" || event.OutputFields["yolo.box"] != "<NA>" || event.OutputFields["user.uid"] != "0" {
		t.Fatal(event)
	}
	for _, part := range []string{"executable=/tmp/yolo-audit-test", "container=host", "box=<NA>", "process=yolo-audit-test", "user=root uid=0", "metadata_available=false"} {
		if !strings.Contains(event.Output, part) {
			t.Fatal("missing rendered field", part, event.Output)
		}
	}
	encoded, _ := json.Marshal(event)
	if strings.Contains(string(encoded), "SECRET_") || len(encoded) > MaxEvent {
		t.Fatal("privacy/bound violation", string(encoded))
	}
	writer, _ := testStore(t)
	writer.Now = func() time.Time { return event.Time.Add(time.Minute) }
	if err := writer.Append(event); err != nil {
		t.Fatal(err)
	}
	summary, err := writer.Scan(Query{}, func(stored Event) error {
		for _, key := range []string{"proc.name", "proc.exepath", "user.name", "user.uid", "container.id", "fd.name", "fs.path.name", "fs.path.target"} {
			if stored.OutputFields[key] != event.OutputFields[key] {
				t.Error("lost retained field", key)
			}
		}
		return nil
	})
	if err != nil || summary.Count != 1 {
		t.Fatal(summary, err)
	}
}

func TestRenderedRuleLegacyAndControlBounds(t *testing.T) {
	event := Event{Rule: "Yolo audit test", Executable: "/tmp/probe", Container: "host", Result: "SUCCESS"}
	message := RenderOutput(event)
	if !strings.Contains(message, "executable=/tmp/probe") || !strings.Contains(message, "process=<NA> user=<NA>") {
		t.Fatal(message)
	}
	fields := make(map[string]string)
	for _, key := range retainedFields {
		fields[key] = strings.Repeat("x", 300) + "\n\x1b[31m"
	}
	raw, _ := json.Marshal(map[string]any{"time": "2026-11-05T00:00:00Z", "rule": "Yolo audit test", "priority": "Notice", "output_fields": fields})
	// Exhaust the selected-field budget without exceeding the input bound.
	parsed, err := ParseEvent(raw)
	if err != nil {
		t.Fatal(err)
	}
	budget := 0
	for _, value := range parsed.OutputFields {
		budget += len(value)
		if strings.ContainsAny(value, "\n\r\x1b") {
			t.Fatal("terminal controls retained")
		}
	}
	encoded, _ := json.Marshal(parsed)
	if budget > 4096 || len(parsed.Output) > 1024 || len(encoded) > MaxEvent {
		t.Fatal(budget, len(parsed.Output), len(encoded))
	}
}
