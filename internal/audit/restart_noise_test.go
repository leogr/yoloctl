package audit

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// Evaluate only the closed conjunction/membership grammar used by setup
// exceptions, separately from official-engine compilation and live capture.
func setupPredicate(t *testing.T, condition string) map[string][]string {
	t.Helper()
	clausePattern := regexp.MustCompile(`^([^\s=]+) (=|in) (.+)$`)
	result := make(map[string][]string)
	for _, clause := range strings.Split(condition, " and ") {
		parts := clausePattern.FindStringSubmatch(strings.TrimSpace(clause))
		if parts == nil {
			t.Fatal("unexpected setup predicate", clause)
		}
		field, op, value := parts[1], parts[2], parts[3]
		if _, exists := result[field]; exists {
			t.Fatal("duplicate setup field", field)
		}
		if op == "in" {
			if !strings.HasPrefix(value, "(") || !strings.HasSuffix(value, ")") {
				t.Fatal("invalid membership", value)
			}
			result[field] = strings.Split(value[1:len(value)-1], ",")
		} else {
			result[field] = []string{value}
		}
		for i, item := range result[field] {
			result[field][i] = strings.Trim(strings.TrimSpace(item), `"`)
		}
	}
	return result
}

func setupMatches(predicate map[string][]string, event map[string]string) bool {
	for field, values := range predicate {
		actual, ok := event[field]
		if !ok || !slices.Contains(values, actual) {
			return false
		}
	}
	return true
}

func TestRuncInitNamespaceException(t *testing.T) {
	entries := escapeEntries(t)
	const macro = "yolo_runc_init_namespace"
	predicate := setupPredicate(t, entries[macro].Condition)
	base := map[string]string{
		"container.label[yoloctl.audit.schema]":       "1",
		"container.label[yoloctl.audit.runtime_mode]": "confined",
		"container.label[yoloctl.openshell.protocol]": "2",
		"proc.exepath": "/usr/local/bin/runc", "proc.name": "runc:[0:PARENT]", "user.uid": "0",
	}
	if len(predicate) != len(base) {
		t.Fatal("runtime context changed", predicate)
	}
	for _, mode := range []string{"false", "true"} {
		for _, name := range []string{"runc:[0:PARENT]", "runc:[1:CHILD]", "runc:[2:INIT]"} {
			event := maps.Clone(base)
			event["proc.name"] = name
			event["container.label[yoloctl.audit.network_unconfined]"] = mode
			if !setupMatches(predicate, event) {
				t.Fatal("expected init not excluded", event)
			}
		}
	}
	for field := range base {
		for _, value := range []string{"<NA>", ""} {
			event := maps.Clone(base)
			event[field] = value
			if setupMatches(predicate, event) {
				t.Fatal("unknown context excluded", field)
			}
		}
		event := maps.Clone(base)
		delete(event, field)
		if setupMatches(predicate, event) {
			t.Fatal("absent context excluded", field)
		}
	}
	for _, tc := range []struct{ field, value string }{
		{"proc.exepath", "/usr/bin/runc"}, {"proc.exepath", "/usr/local/bin/runc-copy"},
		{"proc.exepath", "/usr/bin/unshare"}, {"proc.name", "runc"}, {"proc.name", "runc:[1:CHILD]-copy"},
		{"proc.name", "runc:[3:OTHER]"}, {"user.uid", "502"},
		{"container.label[yoloctl.audit.runtime_mode]", "privileged"},
		{"container.label[yoloctl.audit.schema]", "2"}, {"container.label[yoloctl.openshell.protocol]", "3"},
	} {
		event := maps.Clone(base)
		event[tc.field] = tc.value
		if setupMatches(predicate, event) {
			t.Fatal("unrelated namespace actor excluded", event)
		}
	}
	const original = "evt.type in (mount, umount, umount2, setns, unshare, pivot_root) and yolo_workload and not yolo_openshell_setup"
	if entries["Yolo namespace change"].Condition != original+" and not "+macro {
		t.Fatal("namespace family changed")
	}
	for name, entry := range entries {
		if name != macro && name != "Yolo namespace change" && strings.Contains(entry.Condition, macro) {
			t.Fatal("init exemption leaked to another rule", name)
		}
	}
}

func TestOpenShellStartupDeniedReadException(t *testing.T) {
	entries := escapeEntries(t)
	const macro = "yolo_openshell_startup_read"
	predicate := setupPredicate(t, entries[macro].Condition)
	base := map[string]string{
		"evt.type": "process_vm_readv", "evt.rawres": "-1",
		"container.label[yoloctl.audit.schema]":             "1",
		"container.label[yoloctl.audit.runtime_mode]":       "confined",
		"container.label[yoloctl.audit.network_unconfined]": "false",
		"container.label[yoloctl.openshell.protocol]":       "2",
		"proc.exepath":  "/.openshell/runtime/openshell-sandbox",
		"proc.name":     "openshell-sandb",
		"proc.pexepath": "/opt/yoloctl/openshell-helper/yoloctl-openshell-helper",
	}
	if len(predicate) != len(base) {
		t.Fatal("startup context changed", predicate)
	}
	for _, mode := range []string{"false", "true"} {
		event := maps.Clone(base)
		event["container.label[yoloctl.audit.network_unconfined]"] = mode
		if !setupMatches(predicate, event) {
			t.Fatal("expected EPERM main-thread probe not excluded", event)
		}
	}
	for field := range base {
		for _, value := range []string{"<NA>", ""} {
			event := maps.Clone(base)
			event[field] = value
			if setupMatches(predicate, event) {
				t.Fatal("unknown context excluded", field)
			}
		}
		event := maps.Clone(base)
		delete(event, field)
		if setupMatches(predicate, event) {
			t.Fatal("absent context excluded", field)
		}
	}
	for _, tc := range []struct{ field, value string }{
		{"evt.type", "ptrace"}, {"evt.type", "process_vm_writev"},
		{"evt.rawres", "-14"}, {"evt.rawres", "-13"}, {"evt.rawres", "-38"}, {"evt.rawres", "0"},
		{"proc.name", "openshell-netwo"}, {"proc.name", "openshell-sandb-copy"},
		{"proc.exepath", "/usr/bin/python3"}, {"proc.exepath", "/.openshell/runtime/openshell-supervisor"},
		{"proc.exepath", "/.openshell/runtime/openshell-sandbox-copy"},
		{"proc.pexepath", "/usr/bin/bash"}, {"proc.pexepath", "/.openshell/runtime/openshell-sandbox"},
		{"proc.pexepath", "/opt/yoloctl/openshell-helper/yoloctl-openshell-helper-copy"},
		{"container.label[yoloctl.audit.runtime_mode]", "privileged"},
		{"container.label[yoloctl.audit.schema]", "2"}, {"container.label[yoloctl.openshell.protocol]", "3"},
		{"container.label[yoloctl.audit.network_unconfined]", "unknown"},
	} {
		event := maps.Clone(base)
		event[tc.field] = tc.value
		if setupMatches(predicate, event) {
			t.Fatal("unrelated inspection excluded", event)
		}
	}
	const original = `evt.type in (ptrace, process_vm_readv, process_vm_writev) and yolo_workload and not (container.label[yoloctl.audit.schema] = "1" and container.label[yoloctl.audit.runtime_mode] = confined and proc.exepath in (/usr/bin/gdb, /usr/bin/lldb, /usr/bin/strace, /usr/local/bin/dlv)) and not yolo_openshell_netproxy_read`
	if entries["Yolo process inspection"].Condition != original+" and not "+macro {
		t.Fatal("inspection family changed")
	}
	for name, entry := range entries {
		if name != macro && name != "Yolo process inspection" && strings.Contains(entry.Condition, macro) {
			t.Fatal("startup exemption leaked", name)
		}
	}
}

// Compile both positive network-mode branches with the pinned official engine.
// This is real Falco parsing/filter compilation, not synthetic event replay.
// Live event attribution is a separate runtime acceptance.
func TestPinnedRestartNoiseRuleModes(t *testing.T) {
	archives := os.Getenv("FALCO_TEST_ARCHIVES")
	if archives == "" {
		t.Skip("set FALCO_TEST_ARCHIVES for official engine qualification")
	}
	if runtime.GOOS != "linux" {
		t.Skip("packaged Falco is Linux-only")
	}
	pin, err := Pinned(runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(archives, "falco-"+Version+"-"+pin.Name+".tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	tools, err := Install(root, runtime.GOARCH, raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"false", "true"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			config, err := Configuration(dir, tools, filepath.Join(dir, "metadata.tsv"))
			if err != nil {
				t.Fatal(err)
			}
			var doc map[string]any
			if err := json.Unmarshal(config, &doc); err != nil {
				t.Fatal(err)
			}
			body := Rules
			for _, macro := range []string{"yolo_runc_init_namespace", "yolo_openshell_startup_read"} {
				eventType := "process_vm_readv"
				if macro == "yolo_runc_init_namespace" {
					eventType = "unshare"
				}
				body += "\n- rule: Qualification " + macro + " " + mode + "\n  desc: Compile the explicit network-mode branch\n  condition: " + macro + " and evt.type = " + eventType + " and container.label[yoloctl.audit.network_unconfined] = \"" + mode + "\"\n  output: process=%proc.exepath\n  priority: NOTICE\n"
			}
			rulesPath := filepath.Join(dir, "rules.yaml")
			if err := os.WriteFile(rulesPath, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			doc["rules_files"] = []string{rulesPath}
			config, err = json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			configPath := filepath.Join(dir, "falco.json")
			if err := os.WriteFile(configPath, config, 0600); err != nil {
				t.Fatal(err)
			}
			validation, err := ValidateRuleSet(t.Context(), filepath.Join(tools, "bin", "falco"), configPath)
			t.Log(validation.Diagnostics)
			if err != nil {
				t.Fatal("official Falco mode compilation", err)
			}
		})
	}
}
