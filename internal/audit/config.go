package audit

import (
	_ "embed"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/leogr/yoloctl/internal/auditplugin"
	"github.com/leogr/yoloctl/internal/vmwire"
)

const ServiceName = vmwire.AuditUnitName

// Rules and all output templates are pinned with this build. They never ask
// for argv, environment, aggregate labels or captured buffers.
//
//go:embed rules.yaml
var Rules string

func RulesDigest() string {
	// Field extraction belongs to the loaded configuration, not imported
	// output templates. Old generations must refresh before claiming it.
	return Digest([]byte(Rules + string(auditplugin.Manifest()) +
		"\nmandatory-syscall-output-v1\n" + strings.Join(retainedFields, "\n")))
}

func Configuration(root, tools, metadata string) ([]byte, error) {
	return json.MarshalIndent(map[string]any{
		"rules_files":          []string{filepath.Join(root, "rules.yaml")},
		"engine":               map[string]any{"kind": "modern_ebpf"},
		"time_format_iso_8601": true,
		"json_output":          true, "json_include_output_property": false,
		"json_include_message_property": false, "json_include_output_fields_property": true,
		"json_include_tags_property": false, "buffered_outputs": false,
		"append_output": []any{map[string]any{
			"match":        map[string]string{"source": "syscall"},
			"extra_fields": retainedFields,
		}},
		"stdout_output":  map[string]bool{"enabled": true},
		"file_output":    map[string]bool{"enabled": false},
		"syslog_output":  map[string]bool{"enabled": false},
		"program_output": map[string]any{"enabled": false, "program": ""},
		"http_output":    map[string]bool{"enabled": false},
		"webserver":      map[string]bool{"enabled": false},
		"log_stderr":     true, "log_syslog": false, "log_level": "warning",
		"watch_config_files": false, "load_plugins": []string{"container", "yolo"},
		"plugins": []any{map[string]any{
			"name": "container", "library_path": filepath.Join(tools, "plugins", "libcontainer.so"),
			"init_config": map[string]any{
				"label_max_len": 1024, "with_size": false, "hooks": []string{"create", "start"}, "engine_timeout": 2,
				"engines": map[string]any{
					"containerd": map[string]any{"enabled": true, "sockets": []string{"/run/containerd/containerd.sock"}},
					// In container0.7.6 the CRI matcher also recognizes nerdctl's
					// systemd scope layout. Both engines use only the native system
					// containerd socket; its native API supplies non-CRI metadata.
					"cri":    map[string]any{"enabled": true, "sockets": []string{"/run/containerd/containerd.sock"}},
					"docker": map[string]any{"enabled": false}, "podman": map[string]any{"enabled": false},
					"lxc": map[string]any{"enabled": false}, "libvirt_lxc": map[string]any{"enabled": false}, "bpm": map[string]any{"enabled": false},
				},
			},
		}, map[string]any{"name": "yolo", "library_path": filepath.Join(tools, "plugins", "libyolo.so"), "init_config": metadata}},
		"priority": "debug",
		"metrics":  map[string]any{"enabled": true, "interval": "15s", "output_rule": true, "output_file": "", "rules_counters_enabled": false, "resource_utilization_enabled": false, "state_counters_enabled": false, "kernel_event_counters_enabled": true},
	}, "", "  ")
}

type ServiceConfig struct {
	Epoch           string `json:"epoch"`
	Version         string `json:"version"`
	RulesSHA256     string `json:"rules_sha256"`
	RulesGeneration string `json:"rules_generation,omitempty"`
	EffectiveSHA256 string `json:"effective_sha256,omitempty"`
}

type Health struct {
	Version         string  `json:"version"`
	RulesSHA256     string  `json:"rules_sha256"`
	RulesGeneration string  `json:"rules_generation,omitempty"`
	EffectiveSHA256 string  `json:"effective_sha256,omitempty"`
	Epoch           string  `json:"epoch"`
	State           string  `json:"state"`
	Error           string  `json:"error,omitempty"`
	Loaded          bool    `json:"loaded"`
	DriverDrops     *uint64 `json:"driver_drops,omitempty"`
}
