package audit

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRulesKeepPrivacyAndExplicitNoiseBoundaries(t *testing.T) {
	var entries []struct {
		Rule, Macro, Condition, Output string
		Enabled                        *bool
	}
	if err := yaml.Unmarshal([]byte(Rules), &entries); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"proc.name": true, "proc.exepath": true, "user.name": true, "user.uid": true, "container.id": true, "container.name": true, "evt.res": true, "fd.name": true, "fs.path.name": true, "fs.path.source": true, "fs.path.target": true}
	for _, key := range []string{"box", "space", "agent", "account", "runtime_mode", "network_unconfined", "metadata_available"} {
		allowed["yolo."+key] = true
	}
	fields := regexp.MustCompile(`%([a-z][a-z0-9_.]*(?:\[[^]]+\])?)`)
	rules, disabled := 0, 0
	for _, entry := range entries {
		if entry.Rule == "" {
			continue
		}
		rules++
		if entry.Enabled != nil && !*entry.Enabled {
			disabled++
			if entry.Rule != "Yolo anonymous executable" {
				t.Fatal("unexpected disabled detection", entry.Rule)
			}
		}
		for _, field := range fields.FindAllStringSubmatch(entry.Output, -1) {
			if !allowed[field[1]] {
				t.Fatal("unreviewed persisted field", entry.Rule, field[1])
			}
		}
		if strings.Contains(entry.Condition, "metadata_available") || strings.Contains(entry.Condition, "network_unconfined") {
			t.Fatal("metadata/network setting suppressed rule", entry.Rule)
		}
		if !strings.Contains(entry.Output, "%evt.res") {
			t.Fatal("attempt outcome omitted", entry.Rule)
		}
		for _, field := range []string{"proc.name", "proc.exepath", "user.name", "user.uid", "container.id", "container.name"} {
			if !strings.Contains(entry.Output, "%"+field) {
				t.Fatal("display context omitted", entry.Rule, field)
			}
		}
	}
	if rules != 9 || disabled != 1 {
		t.Fatal(rules, disabled)
	}
	if !strings.Contains(Rules, "not container.id exists") || !strings.Contains(Rules, "yolo_openshell_setup") {
		t.Fatal("unknown context/setup boundary missing")
	}
}

func TestContainerPluginUsesSystemContainerdSocket(t *testing.T) {
	raw, err := Configuration("/private/audit", "/private/tools", "/private/metadata.tsv")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Load    []string `json:"load_plugins"`
		Plugins []struct {
			Name, LibraryPath string
			Init              json.RawMessage `json:"init_config"`
		} `json:"plugins"`
	}
	// Decode exact JSON keys rather than relying on Go's underscore matching.
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Load) != 2 || config.Load[0] != "container" || config.Load[1] != "yolo" || len(config.Plugins) != 2 {
		t.Fatal(string(raw))
	}
	plugin := doc["plugins"].([]any)[0].(map[string]any)
	if plugin["library_path"] != "/private/tools/plugins/libcontainer.so" {
		t.Fatal(plugin)
	}
	engines := plugin["init_config"].(map[string]any)["engines"].(map[string]any)
	for name, engine := range engines {
		item := engine.(map[string]any)
		if item["enabled"] != (name == "containerd" || name == "cri") {
			t.Fatal(name, item)
		}
		if name == "containerd" || name == "cri" {
			sockets := item["sockets"].([]any)
			if len(sockets) != 1 || sockets[0] != "/run/containerd/containerd.sock" {
				t.Fatal(sockets)
			}
		}
	}
	for _, sink := range []string{"file_output", "http_output", "program_output", "syslog_output", "webserver"} {
		if doc[sink].(map[string]any)["enabled"] != false {
			t.Fatal("external sink enabled", sink)
		}
	}
}

func TestEventSelectedPathAndUnknownMetadata(t *testing.T) {
	for _, field := range []string{"fs.path.name", "fs.path.source", "fs.path.target", "fd.name"} {
		raw, _ := json.Marshal(map[string]any{"time": "2026-11-05T00:00:00Z", "rule": "Yolo infrastructure change", "priority": "Warning", "output_fields": map[string]string{field: "/etc/shadow", "container.id": "<NA>", "evt.res": "EACCES"}})
		event, err := ParseEvent(raw)
		if err != nil || event.Path != "/etc/shadow" || event.MetadataAvailable || event.Result != "EACCES" {
			t.Fatal(field, event, err)
		}
	}
}

func TestProtectedPathsIgnoreOnlySymlinkPayloads(t *testing.T) {
	var entries []struct{ Rule, Macro, Condition string }
	if err := yaml.Unmarshal([]byte(Rules), &entries); err != nil {
		t.Fatal(err)
	}
	conditions := make(map[string]string)
	for _, entry := range entries {
		name := entry.Rule
		if name == "" {
			name = entry.Macro
		}
		conditions[name] = strings.Join(strings.Fields(entry.Condition), " ")
	}
	// This is a structural rule regression; pinned Falco separately compiles it.
	// Only symlink payloads are excluded, not real rename/hard-link destinations.
	if conditions["yolo_fs_target_mutation"] != "(not evt.type in (symlink, symlinkat))" {
		t.Fatal("target guard excludes real namespace operations", conditions["yolo_fs_target_mutation"])
	}
	for _, rule := range []struct{ name, list, target string }{
		{"Yolo persistence change", "yolo_persistence", `(yolo_fs_target_mutation and (fs.path.target pmatch (yolo_persistence) or fs.path.target glob "/home/*/.ssh/authorized_keys"))`},
		{"Yolo infrastructure change", "yolo_infrastructure", `(yolo_fs_target_mutation and (fs.path.target pmatch (yolo_infrastructure) or fs.path.target regex "^/mnt/lima-y[a-z2-7]{10}/(config|audit)(/|$)"))`},
	} {
		condition := conditions[rule.name]
		if !strings.Contains(condition, "fs.path.name pmatch ("+rule.list+")") || !strings.Contains(condition, "fs.path.source pmatch ("+rule.list+")") || !strings.Contains(condition, rule.target) {
			t.Fatal("protected link locations or guarded targets lost", rule.name, condition)
		}
		if strings.Contains(strings.Replace(condition, rule.target, "", 1), "fs.path.target") {
			t.Fatal("unguarded link payload can match", rule.name, condition)
		}
		for _, executable := range []string{"containerd", "buildkitd", "nerdctl", "containerd-shim"} {
			if strings.Contains(condition, executable) {
				t.Fatal("blanket runtime exemption", rule.name, executable)
			}
		}
	}
	if !strings.Contains(conditions["Yolo persistence change"], `fs.path.source glob "/home/*/.ssh/authorized_keys"`) {
		t.Fatal("a real authorized_keys symlink location would be missed")
	}
	if !strings.Contains(conditions["Yolo infrastructure change"], "and not yolo_openshell_setup") {
		t.Fatal("known OpenShell setup would report its own private bootstrap writes")
	}
	for _, boundary := range []string{`container.label[yoloctl.audit.schema] = "1"`, `container.label[yoloctl.audit.runtime_mode] = confined`, `container.label[yoloctl.openshell.protocol] = "2"`, "proc.exepath in ("} {
		if !strings.Contains(conditions["yolo_openshell_setup"], boundary) {
			t.Fatal("setup exemption lost its context", boundary)
		}
	}
}
