package audit

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPinnedRuleSetDryRun(t *testing.T) {
	archives := os.Getenv("FALCO_TEST_ARCHIVES")
	if archives == "" {
		t.Skip("set FALCO_TEST_ARCHIVES to qualify the packaged validator")
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
	tools, err := Install(t.TempDir(), runtime.GOARCH, raw)
	if err != nil {
		t.Fatal(err)
	}
	const macro = "- macro: qualification_command\n  condition: evt.type = execve\n"
	const rule = "- rule: Qualification controlled\n  desc: Isolated validator fixture\n  condition: qualification_command\n  output: \"qualification process=%proc.name result=%evt.res\"\n  priority: NOTICE\n  tags: [qualification]\n"
	const override = "- macro: qualification_command\n  condition: evt.type = open\n  override: {condition: replace}\n"
	for _, tc := range []struct {
		name           string
		files          []string
		selection      string
		wantError      bool
		diagnostic     string
		selectionTrace bool
	}{
		{name: "builtin-only"},
		{name: "ordered-dependency", files: []string{macro, rule}},
		{name: "missing-dependency", files: []string{rule}, wantError: true},
		{name: "reversed-dependency", files: []string{rule, macro}, wantError: true},
		{name: "macro-override", files: []string{macro, override, rule}},
		{name: "rule-override", files: []string{macro + rule, "- rule: Qualification controlled\n  condition: evt.type = open\n  override: {condition: replace}\n"}},
		{name: "malformed-condition", files: []string{strings.Replace(macro+rule, "qualification_command\n  output", "(evt.type = )\n  output", 1)}, wantError: true},
		{name: "unknown-field", files: []string{strings.Replace(macro+rule, "qualification_command\n  output", "qualification.unknown = true\n  output", 1)}, wantError: true},
		{name: "wildcard-selection", files: []string{macro + rule}, selection: "rules:\n- disable: {rule: '*'}\n- enable: {rule: 'Qualification*'}\n"},
		{name: "tag-selection", files: []string{macro + rule}, selection: "rules:\n- disable: {rule: '*'}\n- enable: {tag: qualification}\n"},
		{name: "both-selectors", selectionTrace: true, files: []string{macro + rule}, selection: "rules: [{disable: {rule: '*'}}, {enable: {rule: 'Qualification*', tag: qualification}}]\n"},
		{name: "scalar-selectors", selection: "rules: [{enable: {rule: 123, tag: true}}]\n"},
		{name: "nested-selector-alias", selection: "rules:\n- disable: &all {rule: '*'}\n- enable: *all\n"},
		{name: "disable-all", selection: "rules: [{disable: {rule: '*'}}]\n"},
		{name: "invalid-selection", selection: "rules: [{unknown: {rule: '*'}}]\n", wantError: true},
		{name: "unknown-source-warning", files: []string{"- rule: Qualification unknown source\n  desc: Unloaded source fixture\n  source: qualification_missing_plugin\n  condition: evt.type = execve\n  output: qualification\n  priority: NOTICE\n"}, diagnostic: "qualification_missing_plugin"},
		{name: "unused-macro-warning", files: []string{"- macro: qualification_unused_macro\n  condition: evt.type = execve\n"}, diagnostic: "qualification_unused_macro"},
		{name: "unused-list-warning", files: []string{"- list: qualification_unused_list\n  items: [one]\n"}, diagnostic: "qualification_unused_list"},
		{name: "deprecated-append-warning", files: []string{macro + rule, "- rule: Qualification controlled\n  condition: and proc.name = qualification\n  append: true\n"}, diagnostic: "append"},
		{name: "required-engine-mismatch", files: []string{"- required_engine_version: '999.0.0'\n"}, wantError: true},
		{name: "required-plugin-mismatch", files: []string{"- required_plugin_versions:\n  - name: container\n    version: '999.0.0'\n"}, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			config, err := Configuration(root, tools, filepath.Join(root, "metadata.tsv"))
			if err != nil {
				t.Fatal(err)
			}
			var doc map[string]any
			if err := json.Unmarshal(config, &doc); err != nil {
				t.Fatal(err)
			}
			paths := []string{filepath.Join(root, "rules.yaml")}
			if err := os.WriteFile(paths[0], []byte(Rules), 0600); err != nil {
				t.Fatal(err)
			}
			for i, content := range tc.files {
				// Fixed controller-chosen names; user names are never paths.
				name := filepath.Join(root, strings.Repeat("x", len(tc.files)-i)+".yaml")
				if err := os.WriteFile(name, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
				paths = append(paths, name)
			}
			selection, err := SelectionDocument([]byte(tc.selection))
			if err != nil {
				t.Fatal(err)
			}
			selectionPath := filepath.Join(root, "selection.yaml")
			if err := os.WriteFile(selectionPath, selection, 0600); err != nil {
				t.Fatal(err)
			}
			if tc.selectionTrace {
				doc["log_level"] = "info"
			}
			doc["rules_files"] = paths
			doc["config_files"] = []any{map[string]string{"path": selectionPath, "strategy": "override"}}
			doc["rules"] = []any{}
			config, err = json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			configPath := filepath.Join(root, "falco.json")
			if err := os.WriteFile(configPath, config, 0600); err != nil {
				t.Fatal(err)
			}
			// Exactly one validator process per candidate. No -V companion,
			// phase deadline, temporary collector, alternate engine or service.
			validation, err := ValidateRuleSet(t.Context(), filepath.Join(tools, "bin", "falco"), configPath)
			output := []byte(validation.Diagnostics)
			t.Logf("single dry-run err=%v output:\n%s", err, output)
			if (err != nil) != tc.wantError {
				t.Fatalf("single packaged dry-run result: %v", err)
			}
			if tc.selectionTrace {
				previous := -1
				for _, message := range []string{"Disabling rules with name: *", "Enabling rules with name: Qualification*", "Enabling rules with tag: qualification"} {
					index := bytes.Index(output, []byte(message))
					if index <= previous {
						t.Fatal("selection was not evaluated in order", message, string(output))
					}
					previous = index
				}
			}
			if tc.diagnostic != "" && !bytes.Contains(bytes.ToLower(output), []byte(tc.diagnostic)) {
				t.Fatalf("expected Falco warning detail %q", tc.diagnostic)
			}
		})
	}
}
