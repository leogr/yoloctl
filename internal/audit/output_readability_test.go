package audit

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// Pin the previous templates: this change removes two pieces of prose, not
// any of the other selected context or extraction fields.
func TestAuditTemplatesRemoveOnlyRepeatedHeadingAndAvailability(t *testing.T) {
	raw, err := os.ReadFile("testdata/output-templates-4580.json")
	if err != nil {
		t.Fatal(err)
	}
	var before map[string]string
	if err := json.Unmarshal(raw, &before); err != nil {
		t.Fatal(err)
	}
	if len(before) != len(outputTemplates())+1 {
		t.Fatal("template coverage changed")
	}
	for rule, template := range before {
		heading := rule
		if rule == "unknown" {
			heading = "Audit event"
		}
		want := strings.TrimSpace(strings.TrimPrefix(template, heading))
		want = strings.ReplaceAll(want, " metadata_available=%yolo.metadata_available", "")
		if got := outputTemplate(rule); got != want {
			t.Errorf("%s output=%q want=%q", rule, got, want)
		}
	}
}

func TestAuditReadableMessageKeepsStructuredMetadataAndNames(t *testing.T) {
	for rule := range outputTemplates() {
		for _, name := range []string{"abc123", "yolo-worker"} {
			raw, _ := json.Marshal(map[string]any{
				"time": "2026-11-13T20:00:00Z", "rule": rule, "priority": "Notice", "output": "old repeated heading metadata_available=false",
				"output_fields": map[string]any{"container.id": "abc123", "container.name": name, "proc.exepath": "/usr/bin/probe", "yolo.metadata_available": false},
			})
			e, err := ParseEvent(raw)
			if err != nil {
				t.Fatal(err)
			}
			// Older retained Output is intentionally ignored by presentation.
			e.Output = "old repeated heading metadata_available=false"
			text := RenderPrettyOutput(e)
			if strings.Contains(text, rule) || strings.Contains(text, "metadata_available") {
				t.Fatal(rule, text)
			}
			if strings.Contains(text, "container_name=") != (name != e.Container) {
				t.Fatal(name, text)
			}
			if e.OutputFields["container.name"] != name || e.OutputFields["yolo.metadata_available"] != "false" {
				t.Fatal(e.OutputFields)
			}
			stored := RenderOutput(e)
			if !strings.Contains(stored, "container_name="+name) {
				t.Fatal("stored name lost", stored)
			}
			encoded, _ := json.Marshal(e)
			if !strings.Contains(string(encoded), `"metadata_available":false`) || !strings.Contains(string(encoded), `"container.name":"`+name+`"`) {
				t.Fatal(string(encoded))
			}
		}
	}
	e := Event{Rule: "Customer rule", Container: "abc123", OutputFields: map[string]string{"container.name": "abc123"}}
	if text := RenderPrettyOutput(e); strings.Contains(text, "Audit event") || strings.Contains(text, "container_name=") || strings.Contains(text, "metadata_available") {
		t.Fatal(text)
	}
}
