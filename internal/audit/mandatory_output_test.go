package audit

import (
	"encoding/json"
	"testing"

	"github.com/leogr/yoloctl/internal/auditplugin"
)

func TestConfigurationExtractsSelectedFieldsForEverySyscallRule(t *testing.T) {
	raw, err := Configuration("/audit", "/tools", "/metadata.tsv")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Append []struct {
			Match  map[string]string `json:"match"`
			Fields []string          `json:"extra_fields"`
			Output string            `json:"extra_output"`
		} `json:"append_output"`
		IncludeOutput bool `json:"json_include_output_property"`
		IncludeFields bool `json:"json_include_output_fields_property"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	if config.IncludeOutput || !config.IncludeFields {
		t.Fatalf("unsafe output configuration: %s", raw)
	}
	if len(config.Append) != 1 {
		t.Fatalf("all syscall rules need selected extraction, got %s", raw)
	}
	clause := config.Append[0]
	if len(clause.Match) != 1 || clause.Match["source"] != "syscall" || clause.Output != "" {
		t.Fatalf("extraction must not depend on a rule name/tag or alter its text: %+v", clause)
	}
	allowed := make(map[string]bool)
	for _, field := range retainedFields {
		allowed[field] = true
	}
	got := make(map[string]bool)
	for _, field := range clause.Fields {
		if !allowed[field] || got[field] {
			t.Fatalf("unapproved/duplicate extracted field %q", field)
		}
		got[field] = true
	}
	// A minimal imported output may mention none of these fields. The
	// configured extractor must still supply them for authoritative joining.
	for _, field := range []string{"container.id", "proc.exepath", "yolo.box", "yolo.space", "yolo.account", "yolo.agent", "yolo.runtime_mode", "yolo.network_unconfined", "yolo.metadata_available"} {
		if !got[field] {
			t.Fatalf("custom rule lost mandatory context %q", field)
		}
	}
}

func TestMandatoryOutputRejectsPreviousGenerationPins(t *testing.T) {
	// Exact pre-fix identity, which did not request custom-rule metadata.
	legacyRules := Digest(append([]byte(Rules), auditplugin.Manifest()...))
	archives, err := json.Marshal(artifacts)
	if err != nil {
		t.Fatal(err)
	}
	previous := defaultRuleSet()
	previous.Pins = Digest(append([]byte(Version+"\n"+legacyRules+"\n"), archives...))
	if previous.Compatible() {
		t.Fatal("previous extraction configuration can claim current attribution")
	}
}
