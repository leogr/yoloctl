package audit

import (
	_ "embed"
	"encoding/json"
	"path/filepath"

	"github.com/leogr/yoloctl/internal/vmwire"
)

const ServiceName = vmwire.AuditUnitName

// Rules and all output templates are pinned with this build. They never ask
// for argv, environment, aggregate labels or captured buffers.
//
//go:embed rules.yaml
var Rules string

func RulesDigest() string { return Digest([]byte(Rules)) }

func Configuration(root, tools string) ([]byte, error) {
	return json.MarshalIndent(map[string]any{
		"rules_files":          []string{filepath.Join(root, "rules.yaml")},
		"engine":               map[string]any{"kind": "modern_ebpf"},
		"time_format_iso_8601": true,
		"json_output":          true, "json_include_output_property": false,
		"json_include_message_property": false, "json_include_output_fields_property": true,
		"json_include_tags_property": false, "buffered_outputs": false,
		"stdout_output":  map[string]bool{"enabled": true},
		"file_output":    map[string]bool{"enabled": false},
		"syslog_output":  map[string]bool{"enabled": false},
		"program_output": map[string]any{"enabled": false, "program": ""},
		"http_output":    map[string]bool{"enabled": false},
		"webserver":      map[string]bool{"enabled": false},
		"log_stderr":     true, "log_syslog": false, "log_level": "warning",
		"watch_config_files": false, "load_plugins": []string{"container"},
		"plugins": []any{map[string]any{
			"name": "container", "library_path": filepath.Join(tools, "plugins", "libcontainer.so"),
			"init_config": map[string]any{
				"label_max_len": 1024, "with_size": false, "hooks": []string{"create", "start"}, "engine_timeout": 2,
				"engines": map[string]any{
					"containerd": map[string]any{"enabled": true, "sockets": []string{"/run/containerd/containerd.sock"}},
					"cri":        map[string]any{"enabled": false}, "docker": map[string]any{"enabled": false}, "podman": map[string]any{"enabled": false},
					"lxc": map[string]any{"enabled": false}, "libvirt_lxc": map[string]any{"enabled": false}, "bpm": map[string]any{"enabled": false},
				},
			},
		}},
		"priority": "debug",
		"metrics":  map[string]any{"enabled": true, "interval": "15s", "output_rule": true, "output_file": "", "rules_counters_enabled": false, "resource_utilization_enabled": false, "state_counters_enabled": false, "kernel_event_counters_enabled": true},
	}, "", "  ")
}

type ServiceConfig struct {
	Epoch       string `json:"epoch"`
	Version     string `json:"version"`
	RulesSHA256 string `json:"rules_sha256"`
}

type Health struct {
	Version     string  `json:"version"`
	RulesSHA256 string  `json:"rules_sha256"`
	Epoch       string  `json:"epoch"`
	State       string  `json:"state"`
	Error       string  `json:"error,omitempty"`
	Loaded      bool    `json:"loaded"`
	DriverDrops *uint64 `json:"driver_drops,omitempty"`
}
