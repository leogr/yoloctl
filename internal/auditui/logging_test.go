package auditui

import (
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/audit"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

func TestAuditRecordPreservesPriorityWithoutAnOutcome(t *testing.T) {
	for priority, kind := range map[string]prettylog.Kind{
		"Emergency": prettylog.KindError, "Alert": prettylog.KindError,
		"Critical": prettylog.KindError, "Error": prettylog.KindError,
		"Warning": prettylog.KindWarn, "Notice": prettylog.KindNotice,
		"Informational": prettylog.KindInfo, "Debug": prettylog.KindDebug,
	} {
		r := eventRecord(audit.Event{Time: time.Unix(1, 0), Rule: "Yolo audit test", Severity: priority})
		if got := prettylog.KindOf(r); got != kind {
			t.Fatalf("%s maps to %v, want %v", priority, got, kind)
		}
		if prettylog.StructureOf(r).Outcome != "" {
			t.Fatal("an alert was claimed as a command outcome")
		}
		found := false
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "priority" {
				found = a.Value.String() == priority
			}
			return true
		})
		if !found {
			t.Fatalf("lost Falco priority %q", priority)
		}
	}
}

func TestAuditRecordPrivacyAndOriginalEventRemainIntact(t *testing.T) {
	e := audit.Event{Rule: "Yolo audit test", Severity: "Notice", Box: "unproved", Space: "wrong",
		Output: "RAW_SECRET", Container: "host", Executable: "/bin/touch", OutputFields: map[string]string{
			"proc.name": "touch", "proc.args": "RAW_SECRET", "proc.env": "RAW_SECRET", "evt.buffer": "RAW_SECRET",
			"container.labels": "RAW_SECRET", "user.name": "<NA>", "yolo.box": "unproved", "yolo.space": "wrong",
		}}
	before, _ := json.Marshal(e)
	r := eventRecord(e)
	var values []string
	r.Attrs(func(a slog.Attr) bool { values = append(values, a.Key+"="+a.Value.String()); return true })
	text := strings.Join(values, "\n")
	for _, forbidden := range []string{"RAW_SECRET", "unproved", "wrong", "<NA>", "proc.args", "proc.env", "evt.buffer", "container.labels"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("record retained %q: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, "origin=(vm)") || !strings.Contains(text, "proc.name=touch") {
		t.Fatal(text)
	}
	after, _ := json.Marshal(e)
	if string(before) != string(after) {
		t.Fatal("presentation modified JSON/export data")
	}
}

func TestAuditFoldIdentityExcludesOnlyApprovedNonIdentityFields(t *testing.T) {
	e := audit.Event{Rule: "rule", Severity: "Notice", Container: "host", Executable: "/bin/touch", Result: "SUCCESS", Path: "/tmp/a", Endpoint: "127.0.0.1"}
	key := eventKey(e)
	copy := e
	copy.Time = time.Now()
	copy.Cursor = 42
	copy.Output = "not a key"
	copy.Box = "unproved"
	if eventKey(copy) != key {
		t.Fatal("time/cursor/raw output/unproved box changed identity")
	}
	for _, mutate := range []func(*audit.Event){
		func(e *audit.Event) { e.Rule = "other" }, func(e *audit.Event) { e.Severity = "Warning" },
		func(e *audit.Event) { e.Container = "native-container" }, func(e *audit.Event) { e.Executable = "/bin/other" },
		func(e *audit.Event) { e.Result = "EPERM" }, func(e *audit.Event) { e.Path = "/tmp/other" }, func(e *audit.Event) { e.Endpoint = "127.0.0.2" },
		func(e *audit.Event) { e.MetadataAvailable = true; e.Box = "box" },
	} {
		copy = e
		mutate(&copy)
		if eventKey(copy) == key {
			t.Fatalf("different event identity folded: %+v", copy)
		}
	}
	// Separator placement must not alias two fields.
	a, b := e, e
	a.Path = "a|b"
	a.Endpoint = "c"
	b.Path = "a"
	b.Endpoint = "b|c"
	if eventKey(a) == eventKey(b) {
		t.Fatal("ambiguous fold framing")
	}
}
