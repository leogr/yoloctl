package audit

import (
	"maps"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestOpenShellBrokerReadException(t *testing.T) {
	var entries []struct{ Rule, Macro, Condition string }
	if err := yaml.Unmarshal([]byte(Rules), &entries); err != nil {
		t.Fatal(err)
	}
	const macro = "yolo_openshell_netproxy_read"
	var predicate, inspection string
	for _, entry := range entries {
		if entry.Macro == macro {
			if predicate != "" {
				t.Fatal("duplicate broker exception")
			}
			predicate = entry.Condition
		}
		if entry.Rule == "Yolo process inspection" {
			inspection = strings.Join(strings.Fields(entry.Condition), " ")
		} else if strings.Contains(entry.Condition, macro) {
			t.Fatal("broker exception applied to another rule", entry.Rule, entry.Macro)
		}
	}
	// Keep the original host scope and debugger exception verbatim. The new
	// predicate must be an additional exclusion, never a broader setup exemption.
	const original = `evt.type in (ptrace, process_vm_readv, process_vm_writev) and yolo_workload and not (container.label[yoloctl.audit.schema] = "1" and container.label[yoloctl.audit.runtime_mode] = confined and proc.exepath in (/usr/bin/gdb, /usr/bin/lldb, /usr/bin/strace, /usr/local/bin/dlv))`
	if inspection != original+" and not "+macro {
		t.Fatal("process inspection scope changed", inspection)
	}
	// Evaluate only the conjunction of equalities actually read from the YAML.
	// Reject any other grammar rather than pretending to implement Falco. These
	// fixtures are predicate regressions, not engine/live-event qualification;
	// TestPinnedArchivesInstallVerifyAndRuleSyntax separately compiles all rules.
	equality := regexp.MustCompile(`^([^\s=]+) = ("[^"\s]*"|[^\s"=]+)$`)
	required := make(map[string]string)
	for _, clause := range strings.Split(predicate, " and ") {
		match := equality.FindStringSubmatch(strings.TrimSpace(clause))
		if match == nil {
			t.Fatal("exception is not a conjunction of fixed equalities", clause)
		}
		if _, duplicate := required[match[1]]; duplicate {
			t.Fatal("duplicate predicate field", match[1])
		}
		required[match[1]] = strings.Trim(match[2], `"`)
	}
	broker := map[string]string{
		"evt.type": "process_vm_readv", "evt.res": "SUCCESS",
		"container.label[yoloctl.audit.schema]":             "1",
		"container.label[yoloctl.audit.runtime_mode]":       "confined",
		"container.label[yoloctl.audit.network_unconfined]": "false",
		"container.label[yoloctl.openshell.protocol]":       "2",
		"proc.exepath": "/.openshell/runtime/openshell-sandbox",
		"proc.name":    "openshell-netwo",
	}
	if len(required) != len(broker) {
		t.Fatal("broker context lost or expanded", required)
	}
	matches := func(event map[string]string) bool {
		for field, value := range required {
			if actual, present := event[field]; !present || actual != value {
				return false
			}
		}
		return true
	}
	if !matches(broker) {
		t.Fatal("expected successful broker read is not excluded", required)
	}
	for field := range broker {
		t.Run("missing/"+field, func(t *testing.T) {
			event := maps.Clone(broker)
			delete(event, field)
			if matches(event) {
				t.Fatal("absent context suppressed inspection", field)
			}
		})
		t.Run("unknown/"+field, func(t *testing.T) {
			event := maps.Clone(broker)
			event[field] = "<NA>"
			if matches(event) {
				t.Fatal("unknown context suppressed inspection", field)
			}
		})
	}
	for _, tc := range []struct{ name, field, value string }{
		{"ptrace", "evt.type", "ptrace"},
		{"write", "evt.type", "process_vm_writev"},
		{"failed-read", "evt.res", "EFAULT"},
		{"denied-read", "evt.res", "EPERM"},
		{"privileged", "container.label[yoloctl.audit.runtime_mode]", "privileged"},
		{"unconfined-network", "container.label[yoloctl.audit.network_unconfined]", "true"},
		{"wrong-schema", "container.label[yoloctl.audit.schema]", "2"},
		{"wrong-protocol", "container.label[yoloctl.openshell.protocol]", "3"},
		{"renamed-python", "proc.exepath", "/usr/bin/python3"},
		{"ordinary-process", "proc.exepath", "/usr/bin/curl"},
		{"supervisor", "proc.exepath", "/.openshell/runtime/openshell-supervisor"},
		{"helper", "proc.exepath", "/opt/yoloctl/openshell-helper/yoloctl-openshell-helper"},
		{"startup-probe", "proc.name", "openshell-sandb"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event := maps.Clone(broker)
			event[tc.field] = tc.value
			if matches(event) {
				t.Fatal("unrelated inspection received broker exception", event)
			}
		})
	}
}
