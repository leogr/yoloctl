package audit

import (
	"bytes"
	"strings"
	"testing"
)

func TestSelectionDocumentPreservesFalcoSelectorBytes(t *testing.T) {
	for _, raw := range []string{
		"rules: []\n",
		"# comments stay\nrules:\n- disable: {rule: '*'}\n- enable: {tag: developer}\n",
		"rules: [{enable: {rule: 'Yolo*', tag: true}}]\n",
		"rules:\n  - enable:\n      rule: 123\n      tag: false\n",
		"rules:\r\n- enable: {tag: developer}\r\n",
		"#" + strings.Repeat(" long comment", 10000) + "\nrules: []\n",
	} {
		got, err := SelectionDocument([]byte(raw))
		if err != nil || !bytes.Equal(got, []byte(raw)) {
			t.Fatal("selection bytes changed", err)
		}
	}
	for _, raw := range []string{"", "\n # only comments\n"} {
		got, err := SelectionDocument([]byte(raw))
		if err != nil || string(got) != "rules: []\n" {
			t.Fatal(string(got), err)
		}
	}
}

func TestSelectionDocumentRejectsConfigurationEscapes(t *testing.T) {
	for name, raw := range map[string]string{
		"second-root":     "rules: []\nstdout_output: {enabled: true}\n",
		"second-document": "rules: []\n---\nhttp_output: {enabled: true}\n",
		"end-document":    "rules: []\n...\n",
		"directive":       "%YAML 1.1\nrules: []\n",
		"bom":             "\xef\xbb\xbfrules: []\n",
		"tab-indent":      "rules:\n\t- enable: {rule: '*'}\n",
		"flow-root":       "{rules: []}\n",
		"explicit-key":    "? rules\n: []\n",
		"indented-root":   " rules: []\n",
		"duplicate-root":  "rules: []\nrules: []\n",
		"include":         "rules: []\nconfig_files: [/elsewhere]\n",
		"root-alias":      "rules: *external\n",
		"merge-key":       "rules: []\n<<: *external\n",
		"scalar":          "rules: disabled\n",
		"null":            "rules:\n",
		"mapping":         "rules: {disable: '*'}\n",
		"bad-yaml":        "rules: [\n",
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := SelectionDocument([]byte(raw)); err == nil || got != nil {
				t.Fatal("configuration escape accepted", string(got), err)
			}
		})
	}
}
