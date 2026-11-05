package audit

import (
	"encoding/json"
	"path/filepath"

	"github.com/leogr/yoloctl/internal/vmwire"
)

const ServiceName = vmwire.AuditUnitName

// S1's deliberately narrow test rule is replaced by the yolo ruleset in S2.
// No output template requests argv, environment, buffers or Falco's raw output.
const Rules = `- rule: Yolo audit test
  desc: Controlled audit pipeline acceptance event
  condition: evt.type = execve and proc.name = yolo-audit-test
  output: "Audit test executable=%proc.exepath result=%evt.res"
  priority: NOTICE
  tags: [yoloctl]
`

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
		"program_output": map[string]bool{"enabled": false},
		"http_output":    map[string]bool{"enabled": false},
		"grpc_output":    map[string]bool{"enabled": false},
		"webserver":      map[string]bool{"enabled": false},
		"grpc":           map[string]bool{"enabled": false},
		"log_stderr":     true, "log_syslog": false, "log_level": "warning",
		"watch_config_files": false, "load_plugins": []string{},
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
