package agentbox

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/audit"
)

func TestAuditLabelsEnrichWithoutChangingReconciliation(t *testing.T) {
	for _, test := range []struct {
		account, mode, network string
		privileged, unconfined bool
	}{
		{"none", "confined", "false", false, false},
		{"personal", "confined", "true", false, true},
		{"personal", "privileged", "false", true, false},
	} {
		t.Run(test.mode+test.network, func(t *testing.T) {
			original := LabelSet{Space: "work", Agentbox: "worker", Agent: "none", Account: test.account, Privileged: test.privileged, UnconfinedNetwork: test.unconfined}
			labels, err := EncodeLabels(original)
			if err != nil {
				t.Fatal(err)
			}
			if labels[LabelAuditSchema] != "1" || labels[LabelAuditAccount] != test.account || labels[LabelAuditRuntimeMode] != test.mode || labels[LabelAuditNetworkUnconfined] != test.network {
				t.Fatal(labels)
			}
			decoded := DecodeLabels(labels)
			if decoded.Space != original.Space || decoded.Agentbox != original.Agentbox || decoded.Privileged != original.Privileged || decoded.UnconfinedNetwork != original.UnconfinedNetwork {
				t.Fatal("audit labels changed live reconstruction", decoded)
			}
			if test.account == "none" && labels[LabelAccount] != "" {
				t.Fatal("legacy absent account changed")
			}
			if !test.unconfined && labels[LabelNetworkUnconfined] != "" {
				t.Fatal("legacy absent network changed")
			}
			for _, scenario := range []string{"known", "legacy", "missing", "other-kind", "bad-schema", "host", "stale-plugin", "invalid-name"} {
				fields := map[string]any{"container.id": "0123456789ab", "proc.exepath": "/bin/sh", "proc.args": "SECRET_ARGV", "proc.env": "SECRET_ENV", "container.labels": "SECRET_AGGREGATE", "evt.res": "EPERM"}
				for k, v := range labels {
					fields["container.label["+k+"]"] = v
				}
				switch scenario {
				case "legacy":
					delete(fields, "container.label["+LabelAuditSchema+"]")
				case "missing":
					delete(fields, "container.label["+LabelAuditAccount+"]")
				case "other-kind":
					fields["container.label["+LabelKind+"]"] = "unmanaged"
				case "bad-schema":
					fields["container.label["+LabelAuditSchema+"]"] = "2"
				case "host":
					fields["container.id"] = "host"
				case "stale-plugin":
					fields["yolo.metadata_available"] = false
				case "invalid-name":
					fields["container.label["+LabelAgentbox+"]"] = "../escape"
				}
				raw, err := json.Marshal(map[string]any{"time": "2026-11-05T03:00:00Z", "rule": "Yolo kernel instrumentation", "priority": "Warning", "output": "SECRET_OUTPUT", "output_fields": fields})
				if err != nil {
					t.Fatal(err)
				}
				event, err := audit.ParseEvent(raw)
				if err != nil {
					t.Fatal(err)
				}
				known := scenario == "known"
				if event.MetadataAvailable != known {
					t.Fatal(scenario, event)
				}
				if known && (event.Box != "worker" || event.Space != "work" || event.Account != test.account || event.RuntimeMode != test.mode || event.NetworkUnconfined != test.network) {
					t.Fatal(event)
				}
				if !known && (event.Box != "" || event.Account != "" || event.RuntimeMode != "") {
					t.Fatal("unknown context invented identity", scenario, event)
				}
				if event.Rule == "" || event.Result != "EPERM" {
					t.Fatal("missing metadata suppressed event", event)
				}
				safe, _ := json.Marshal(event)
				for _, secret := range []string{"SECRET_ARGV", "SECRET_ENV", "SECRET_AGGREGATE", "SECRET_OUTPUT"} {
					if strings.Contains(string(safe), secret) {
						t.Fatal("persisted secret", secret)
					}
				}
			}
		})
	}
}
