package audit

import (
	"maps"
	"strings"
	"testing"
)

func TestRuncInitWithoutNativeContainerLabels(t *testing.T) {
	predicate := setupPredicate(t, escapeEntries(t)["yolo_runc_init_namespace"].Condition)
	for _, mode := range []string{"confined", "privileged", "<NA>"} {
		for _, network := range []string{"false", "true"} {
			for _, name := range []string{"runc:[0:PARENT]", "runc:[1:CHILD]", "runc:[2:INIT]"} {
				event := map[string]string{
					"proc.exepath": "/usr/local/bin/runc", "proc.pexepath": "/usr/local/bin/runc",
					"proc.name": name, "user.uid": "0", "yolo.runtime_mode": mode,
					"yolo.network_unconfined": network,
				}
				if !setupMatches(predicate, event) {
					t.Fatalf("runtime init still depends on unavailable native labels: %v", event)
				}
			}
		}
	}
}

func TestOpenShellSandboxParentDeniedSelfCheck(t *testing.T) {
	entries := escapeEntries(t)
	const macro = "yolo_openshell_self_check_read"
	predicate := setupPredicate(t, entries[macro].Condition)
	base := map[string]string{
		"evt.type": "process_vm_readv", "evt.rawres": "-1",
		"proc.exepath":  "/.openshell/runtime/openshell-sandbox",
		"proc.pexepath": "/.openshell/runtime/openshell-sandbox", "proc.name": "openshell-sandb",
	}
	if len(predicate) != len(base) {
		t.Fatal("self-check selection changed", predicate)
	}
	for _, mode := range []string{"false", "true"} {
		event := maps.Clone(base)
		event["yolo.network_unconfined"] = mode
		if !setupMatches(predicate, event) {
			t.Fatal("unlabelled denied sandbox self-check not excluded", event)
		}
	}
	for field := range base {
		event := maps.Clone(base)
		delete(event, field)
		if setupMatches(predicate, event) {
			t.Fatal("missing identity excluded", field)
		}
		for _, unknown := range []string{"", "<NA>"} {
			event := maps.Clone(base)
			event[field] = unknown
			if setupMatches(predicate, event) {
				t.Fatal("unknown identity excluded", field)
			}
		}
	}
	for _, tc := range []struct{ field, value string }{
		{"evt.type", "ptrace"}, {"evt.type", "process_vm_writev"},
		{"evt.rawres", "0"}, {"evt.rawres", "23"}, {"evt.rawres", "-13"}, {"evt.rawres", "-14"},
		{"proc.exepath", "/usr/bin/python3"}, {"proc.exepath", "/.openshell/runtime/openshell-sandbox-copy"},
		{"proc.exepath", "/.openshell/runtime/openshell-supervisor"},
		{"proc.name", "openshell-netwo"}, {"proc.name", "openshell-sandb-copy"},
		{"proc.pexepath", "/bin/sh"}, {"proc.pexepath", "/opt/yoloctl/openshell-helper/yoloctl-openshell-helper"},
		{"proc.pexepath", "/.openshell/runtime/openshell-sandbox-copy"},
	} {
		event := maps.Clone(base)
		event[tc.field] = tc.value
		if setupMatches(predicate, event) {
			t.Fatal("other inspection excluded", event)
		}
	}
	if !strings.HasSuffix(entries["Yolo process inspection"].Condition, " and not "+macro) {
		t.Fatal("self-check exclusion is not scoped to inspection")
	}
	for name, entry := range entries {
		if name != macro && name != "Yolo process inspection" && strings.Contains(entry.Condition, macro) {
			t.Fatal("self-check exclusion leaked to another family", name)
		}
	}
}
