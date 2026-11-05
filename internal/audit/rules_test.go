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
	allowed := map[string]bool{"proc.exepath": true, "container.id": true, "evt.res": true, "fd.name": true, "fs.path.name": true, "fs.path.source": true, "fs.path.target": true}
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
	}
	if rules != 9 || disabled != 1 {
		t.Fatal(rules, disabled)
	}
	if !strings.Contains(Rules, "not container.id exists") || !strings.Contains(Rules, "yolo_openshell_setup") {
		t.Fatal("unknown context/setup boundary missing")
	}
}

func TestContainerPluginUsesNativeContainerdOnly(t *testing.T) {
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
		if item["enabled"] != (name == "containerd") {
			t.Fatal(name, item)
		}
		if name == "containerd" {
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
