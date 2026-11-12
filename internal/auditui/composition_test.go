package auditui

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/audit"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

// Rules management adds custom rule names and escape rules retain numeric
// fields. Both must survive D-falco's structured projection without admitting
// the imported output template or private fields into either human view.
func TestCustomRulesComposeWithStructuredPresentation(t *testing.T) {
	for _, rule := range []string{"Customer defined rule", "Yolo UID set to root"} {
		t.Run(rule, func(t *testing.T) {
			raw, err := json.Marshal(map[string]any{
				"time": "2026-11-12T01:00:00Z", "rule": rule, "priority": "Warning",
				"output": "RAW_TEMPLATE_SECRET",
				"output_fields": map[string]any{
					"proc.exepath": "/usr/bin/example", "proc.name": "example",
					"user.uid": 1000, "evt.arg.uid": 0, "evt.res": "SUCCESS",
					"container.id": "host", "proc.cmdline": "ARGV_SECRET",
					"proc.env": "ENV_SECRET", "evt.args": "AGGREGATE_SECRET",
					"unknown.field": "UNKNOWN_SECRET",
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			event, err := audit.ParseEvent(raw)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			options := EventOptions{Width: 240, Verbose: true}
			printer := NewLogPrinter(t.Context(), &out, prettylog.Normal, options)
			t.Cleanup(func() { _ = printer.Close() })
			for range 2 {
				if err := printer.Add(t.Context(), event); err != nil {
					t.Fatal(err)
				}
			}
			if err := printer.Close(); err != nil {
				t.Fatal(err)
			}
			text := out.String()
			if strings.Count(text, "01:00:00  ! Warning") != 1 || strings.Count(text, "repeated 1 more time") != 1 {
				t.Fatal("custom event or final shared summary lost", text)
			}
			for _, rendered := range []string{text, EventText(event, options)} {
				for _, want := range []string{rule, "(vm)", "/usr/bin/example", "1000", "evt.arg.uid="} {
					if !strings.Contains(rendered, want) {
						t.Fatalf("missing %q: %s", want, rendered)
					}
				}
				for _, secret := range []string{"RAW_TEMPLATE_SECRET", "ARGV_SECRET", "ENV_SECRET", "AGGREGATE_SECRET", "UNKNOWN_SECRET", "<NA>"} {
					if strings.Contains(rendered, secret) {
						t.Fatalf("presentation leaked %q: %s", secret, rendered)
					}
				}
			}
			if rule == "Customer defined rule" && !strings.Contains(text, "Audit event") {
				t.Fatal("custom fixed-field fallback lost", text)
			}
		})
	}
}
