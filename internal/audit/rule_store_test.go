package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/safepath"
)

func TestRuleStoreOrderedGenerations(t *testing.T) {
	store := RuleStore{Root: t.TempDir()}
	initial, err := store.Read("")
	if err != nil || len(initial.Files) != 1 || !initial.Files[0].Builtin {
		t.Fatalf("initial %v: %v", initial, err)
	}
	if _, err := os.Stat(layout.AuditRulesDir(store.Root)); !os.IsNotExist(err) {
		t.Fatalf("read allocated storage: %v", err)
	}
	calls := 0
	validate := func(ctx context.Context, binary, config string) (Validation, error) {
		calls++
		if binary != "/tools/bin/falco" {
			t.Fatal(binary)
		}
		raw, err := os.ReadFile(config)
		if err != nil {
			t.Fatal(err)
		}
		var fields struct {
			Files    []string                          `json:"rules_files"`
			Includes []struct{ Path, Strategy string } `json:"config_files"`
		}
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		if len(fields.Files) != calls+1 || len(fields.Includes) != 1 || fields.Includes[0].Strategy != "override" {
			t.Fatalf("config %s", raw)
		}
		for i, path := range fields.Files {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if i == 0 && string(raw) != Rules {
				t.Fatal("builtin changed")
			}
			if i > 0 && string(raw) != fmt.Sprintf("opaque %d\n", i) {
				t.Fatalf("order %d: %q", i, raw)
			}
		}
		return Validation{Warnings: 2, Diagnostics: "2 Warnings:\n"}, nil
	}
	a, _, changed, err := store.Prepare(t.Context(), "", RuleChange{Kind: "import", Name: "../../no.yaml", Body: strings.NewReader("opaque 1\n")}, "/tools", "/metadata", validate)
	if err == nil || changed || a.Generation != "" {
		t.Fatal("accepted path as basename")
	}
	a, _, changed, err = store.Prepare(t.Context(), "", RuleChange{Kind: "import", Name: "same.yaml", Body: strings.NewReader("opaque 1\n")}, "/tools", "/metadata", validate)
	if err != nil || !changed || calls != 1 {
		t.Fatalf("first %v %v", changed, err)
	}
	b, _, changed, err := store.Prepare(t.Context(), a.Generation, RuleChange{Kind: "import", Name: "same.yaml", Body: strings.NewReader("opaque 2\n")}, "/tools", "/metadata", validate)
	if err != nil || !changed || calls != 2 {
		t.Fatalf("second %v %v", changed, err)
	}
	if _, err := store.Read(b.Generation); err != nil {
		t.Fatal(err)
	}
	reread, err := store.Read(a.Generation)
	if err != nil || !reflect.DeepEqual(reread, a) {
		t.Fatal("changed original generation", err)
	}
	noop, _, changed, err := store.Prepare(t.Context(), b.Generation, RuleChange{Kind: "import", Name: "other.yaml", Body: strings.NewReader("opaque 1\n")}, "/tools", "/metadata", validate)
	if err != nil || changed || noop.Generation != b.Generation || calls != 2 {
		t.Fatalf("duplicate was not no-op %v %v", changed, err)
	}
	for _, change := range []RuleChange{{Kind: "remove", Name: "same.yaml"}, {Kind: "remove", Name: "same.yaml", SHA256: b.Files[1].SHA256[:7]}, {Kind: "remove", Name: "rules.yaml"}} {
		if _, _, _, err := store.Prepare(t.Context(), b.Generation, change, "/tools", "/metadata", validate); err == nil {
			t.Fatalf("accepted %#v", change)
		}
	}
	accepted := func(context.Context, string, string) (Validation, error) { calls++; return Validation{}, nil }
	c, _, changed, err := store.Prepare(t.Context(), b.Generation, RuleChange{Kind: "remove", Name: "same.yaml", SHA256: strings.ToUpper(b.Files[1].SHA256[:9])}, "/tools", "/metadata", accepted)
	if err != nil || !changed || len(c.Files) != 2 || c.Files[1] != b.Files[2] {
		t.Fatal("remove changed surviving order", err)
	}
	if _, err := store.Configuration(c.Generation, "/tools", "/metadata"); err != nil {
		t.Fatal(err)
	}
	dir, _ := layout.AuditRulesGeneration(store.Root, c.Generation)
	if err := safepath.WriteFile(dir, "falco.json", []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Configuration(c.Generation, "/tools", "/metadata"); err == nil {
		t.Fatal("changed envelope accepted")
	}
}

func TestRuleStoreSelectionAndRejectedAttempt(t *testing.T) {
	store := RuleStore{Root: t.TempDir()}
	calls := 0
	validate := func(context.Context, string, string) (Validation, error) { calls++; return Validation{}, nil }
	raw := []byte("# verbatim\nrules:\n- enable:\n    rule: '*'")
	a, _, _, err := store.Prepare(t.Context(), "", RuleChange{Kind: "edit", Body: bytes.NewReader(raw)}, "/tools", "/metadata", validate)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Selection, raw) {
		t.Fatal("selection was translated")
	}
	if _, _, changed, err := store.Prepare(t.Context(), a.Generation, RuleChange{Kind: "edit", Body: bytes.NewReader(raw)}, "/tools", "/metadata", validate); err != nil || changed || calls != 1 {
		t.Fatal("unchanged selection validated", err)
	}
	for _, bad := range []string{"rules: []\nstdout_output: true\n", "rules: []\n---\nrules: []\n"} {
		if _, _, _, err := store.Prepare(t.Context(), a.Generation, RuleChange{Kind: "edit", Body: strings.NewReader(bad)}, "/tools", "/metadata", validate); err == nil {
			t.Fatal("accepted escape")
		}
	}
	rejected := errors.New("native rule error")
	rejectedValidate := func(context.Context, string, string) (Validation, error) {
		return Validation{Diagnostics: "Falco explanation"}, rejected
	}
	_, diag, _, err := store.Prepare(t.Context(), a.Generation, RuleChange{Kind: "import", Name: "bad.yaml", Body: strings.NewReader("opaque malformed")}, "/tools", "/metadata", rejectedValidate)
	if !errors.Is(err, rejected) || diag.Diagnostics != "Falco explanation" {
		t.Fatal("lost native diagnostics", err)
	}
	entries, err := os.ReadDir(layout.AuditRulesDir(store.Root))
	if err != nil || len(entries) != 1 || entries[0].Name() != a.Generation {
		t.Fatal("failed generation survived", entries, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	_, _, _, err = store.Prepare(ctx, a.Generation, RuleChange{Kind: "refresh"}, "/tools", "/metadata", func(context.Context, string, string) (Validation, error) { cancel(); return Validation{}, nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	entries, _ = os.ReadDir(layout.AuditRulesDir(store.Root))
	if len(entries) != 1 {
		t.Fatal("cancelled generation survived")
	}
	preserved, err := store.Read(a.Generation)
	if err != nil || !reflect.DeepEqual(preserved, a) {
		t.Fatal("invalid/cancelled mutation changed selected state", err)
	}
}

func TestRuleStoreSingleFileLimitAndUnlimitedSelection(t *testing.T) {
	store := RuleStore{Root: t.TempDir()}
	calls := 0
	validate := func(context.Context, string, string) (Validation, error) { calls++; return Validation{}, nil }
	a, _, _, err := store.Prepare(t.Context(), "", RuleChange{Kind: "import", Name: "exact.yaml", Body: strings.NewReader(strings.Repeat("#", MaxRuleFile))}, "/tools", "/metadata", validate)
	if err != nil || calls != 1 {
		t.Fatal("exact2MiB rejected", err)
	}
	_, _, _, err = store.Prepare(t.Context(), a.Generation, RuleChange{Kind: "import", Name: "large.yaml", Body: strings.NewReader(strings.Repeat("#", MaxRuleFile+1))}, "/tools", "/metadata", validate)
	if err == nil || calls != 1 {
		t.Fatal("oversized file validated")
	}
	selection := "rules: []\n#" + strings.Repeat("long selection ", 160000)
	b, _, _, err := store.Prepare(t.Context(), a.Generation, RuleChange{Kind: "edit", Body: strings.NewReader(selection)}, "/tools", "/metadata", validate)
	if err != nil || string(b.Selection) != selection {
		t.Fatal("rule-file capacity leaked into selection", err)
	}
}

func TestRuleStoreRejectsChangedAndSymlinkedData(t *testing.T) {
	store := RuleStore{Root: t.TempDir()}
	set, _, _, err := store.Prepare(t.Context(), "", RuleChange{Kind: "import", Name: "a.yaml", Body: strings.NewReader("opaque")}, "/tools", "/metadata", func(context.Context, string, string) (Validation, error) { return Validation{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := layout.AuditRulesGeneration(store.Root, set.Generation)
	imported := filepath.Join(dir, ruleFilePath(set.Files[1]))
	if err := os.WriteFile(imported, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(set.Generation); err == nil {
		t.Fatal("changed file accepted")
	}
	if err := os.Remove(imported); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "rules.yaml"), imported); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(set.Generation); err == nil {
		t.Fatal("symlink accepted")
	}
	if _, err := store.Read(strings.Repeat("a", 64)); err == nil {
		t.Fatal("missing selected generation fell back")
	}
	if _, err := store.Read("../escape"); err == nil {
		t.Fatal("generation traversal")
	}
}

func TestRuleStoreExplicitUpgradeKeepsRetainedData(t *testing.T) {
	store := RuleStore{Root: t.TempDir()}
	calls := 0
	validate := func(context.Context, string, string) (Validation, error) { calls++; return Validation{}, nil }
	a, _, _, err := store.Prepare(t.Context(), "", RuleChange{Kind: "import", Name: "kept.yaml", Body: strings.NewReader("opaque retained bytes")}, "/tools", "/metadata", validate)
	if err != nil {
		t.Fatal(err)
	}
	a.Pins = strings.Repeat("a", 64)
	a.Fingerprint = a.digest()
	dir, _ := layout.AuditRulesGeneration(store.Root, a.Generation)
	raw, _ := json.Marshal(a)
	if err := safepath.WriteFile(dir, "manifest.json", raw, 0600); err != nil {
		t.Fatal(err)
	}
	retained, err := store.Read(a.Generation)
	if err != nil || retained.Compatible() {
		t.Fatal("incompatible generation hidden", err)
	}
	if _, err := store.Configuration(a.Generation, "/tools", "/metadata"); err == nil {
		t.Fatal("implicit incompatible activation")
	}
	upgraded, _, _, err := store.Prepare(t.Context(), a.Generation, RuleChange{Kind: "refresh"}, "/tools", "/metadata", validate)
	if err != nil || !upgraded.Compatible() || upgraded.Files[1] != a.Files[1] || !bytes.Equal(upgraded.Selection, a.Selection) || calls != 2 {
		t.Fatal("upgrade discarded custom data or skipped validation", err)
	}
	retained, err = store.Read(a.Generation)
	if err != nil || retained.Compatible() {
		t.Fatal("upgrade mutated prior generation", err)
	}
}
