package spacearchive

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/audit"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"gopkg.in/yaml.v3"
)

func auditRuleArchiveFixture(t *testing.T) ([]byte, string, *spaceconfig.Bootstrap, audit.RuleSet) {
	t.Helper()
	source, boot := fixture(t)
	store := audit.RuleStore{Root: layout.AuditDiskDir(source)}
	accept := func(context.Context, string, string) (audit.Validation, error) {
		return audit.Validation{Warnings: 2}, nil
	}
	a, _, _, err := store.Prepare(t.Context(), "", audit.RuleChange{Kind: "import", Name: "same.yaml", Body: strings.NewReader("- macro: a\n  condition: evt.type = execve\n")}, "/not-exported-tools", "/not-exported-metadata", accept)
	if err != nil {
		t.Fatal(err)
	}
	b, _, _, err := store.Prepare(t.Context(), a.Generation, audit.RuleChange{Kind: "import", Name: "same.yaml", Body: strings.NewReader("# second opaque file\n")}, "/not-exported-tools", "/not-exported-metadata", accept)
	if err != nil {
		t.Fatal(err)
	}
	set, _, _, err := store.Prepare(t.Context(), b.Generation, audit.RuleChange{Kind: "edit", Body: strings.NewReader("# unchanged source bytes\nrules:\n- disable: {rule: '*'}\n")}, "/not-exported-tools", "/not-exported-metadata", accept)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := readDeclarations(source, boot)
	if err != nil {
		t.Fatal(err)
	}
	bundle.Settings.Audit = &spaceconfig.AuditSettings{Enabled: true, Version: audit.Version, RulesSHA256: audit.RulesDigest(), RulesGeneration: set.Generation, EffectiveSHA256: set.Fingerprint}
	raw, _ := yaml.Marshal(bundle.Settings)
	if err := safepath.WriteFile(filepath.Join(source, "config"), "space.yaml", raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"tools/private.log", "logs/private.log", "service.json"} {
		if err := safepath.WriteFile(store.Root, path, []byte("NOT_FOR_RULE_ARCHIVE"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := WriteState(t.Context(), &out, source, boot); err != nil {
		t.Fatal(err)
	}
	after, err := safepath.ReadFile(filepath.Join(source, "config"), "space.yaml", 1<<20)
	if err != nil || !bytes.Equal(after, raw) {
		t.Fatal("export changed source enablement", err)
	}
	return out.Bytes(), source, boot, set
}

func TestAuditRuleArchiveRoundTripRemainsDisabled(t *testing.T) {
	raw, source, boot, want := auditRuleArchiveFixture(t)
	if bytes.Contains(raw, []byte("NOT_FOR_RULE_ARCHIVE")) || bytes.Contains(raw, []byte("/not-exported-tools")) {
		t.Fatal("tools/logs/generated config leaked")
	}
	seed, err := inspectState(t.Context(), bytes.NewReader(raw), boot, nil)
	if err != nil {
		t.Fatal(err)
	}
	if seed.Settings.Audit == nil || seed.Settings.Audit.Enabled || seed.Settings.Audit.RulesGeneration != want.Generation {
		t.Fatal("rule identity lost or enablement inherited", seed.Settings.Audit)
	}
	target := t.TempDir()
	if err := os.Mkdir(filepath.Join(target, "account-state"), 0700); err != nil {
		t.Fatal(err)
	}
	next := *boot
	next.Name = "copy"
	next.DiskID = strings.Repeat("b", 32)
	next.DiskUUID = "fresh"
	next.UID = os.Geteuid()
	next.GID = os.Getegid()
	digest := sha256.Sum256(raw)
	if err := RestoreState(t.Context(), bytes.NewReader(raw), target, &next, hex.EncodeToString(digest[:]), int64(len(raw))); err != nil {
		t.Fatal(err)
	}
	restoredStore := audit.RuleStore{Root: layout.AuditDiskDir(target)}
	got, err := restoredStore.Read(want.Generation)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("rule order/metadata/selection changed", err)
	}
	for _, file := range want.Files {
		a, err := (audit.RuleStore{Root: layout.AuditDiskDir(source)}).FileBytes(want, file)
		if err != nil {
			t.Fatal(err)
		}
		b, err := restoredStore.FileBytes(got, file)
		if err != nil || !bytes.Equal(a, b) {
			t.Fatal("rule bytes changed", err)
		}
	}
	dir, _ := layout.AuditRulesGeneration(restoredStore.Root, got.Generation)
	for _, path := range []string{filepath.Join(restoredStore.Root, "tools"), filepath.Join(restoredStore.Root, "logs"), filepath.Join(restoredStore.Root, "service.json"), filepath.Join(dir, "falco.json")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("restored infrastructure instead of source", path, err)
		}
	}
	if err := restoredStore.Restore(t.Context(), want, func(audit.RuleFile) ([]byte, error) {
		t.Fatal("identical pending import recopied data")
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAuditRuleArchiveRefusesInvalidInventory(t *testing.T) {
	raw, _, boot, set := auditRuleArchiveFixture(t)
	custom := pathForAuditTest(set.Files[1])
	for _, scenario := range []string{"changed-bytes", "missing-file", "extra-file", "missing-manifest", "not-private", "symlink", "enabled", "changed-selection"} {
		t.Run(scenario, func(t *testing.T) {
			var out bytes.Buffer
			tw := tar.NewWriter(&out)
			tr := tar.NewReader(bytes.NewReader(raw))
			for {
				h, err := tr.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				data, err := io.ReadAll(tr)
				if err != nil {
					t.Fatal(err)
				}
				copy := *h
				if h.Name == custom {
					switch scenario {
					case "changed-bytes":
						data = []byte("changed")
					case "missing-file":
						continue
					case "not-private":
						copy.Mode = 0644
					case "symlink":
						copy.Typeflag = tar.TypeSymlink
						copy.Linkname = "/etc/shadow"
						data = nil
					}
				}
				if h.Name == "audit-rules/manifest.json" {
					if scenario == "missing-manifest" {
						continue
					}
					if scenario == "changed-selection" {
						var doc map[string]any
						if err := json.Unmarshal(data, &doc); err != nil {
							t.Fatal(err)
						}
						doc["selection"] = []byte("rules: []\n")
						data, _ = json.Marshal(doc)
					}
				}
				if h.Name == "declarations.json" && scenario == "enabled" {
					var doc map[string]any
					if err := json.Unmarshal(data, &doc); err != nil {
						t.Fatal(err)
					}
					settings := doc["settings"].(map[string]any)
					settings["audit"].(map[string]any)["enabled"] = true
					data, _ = json.Marshal(doc)
				}
				copy.Size = int64(len(data))
				if err := tw.WriteHeader(&copy); err != nil {
					t.Fatal(err)
				}
				if _, err := tw.Write(data); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "extra-file" {
				if err := tw.WriteHeader(&tar.Header{Name: "audit-rules/unlisted.yaml", Typeflag: tar.TypeReg, Mode: 0600, Size: 1}); err != nil {
					t.Fatal(err)
				}
				if _, err := tw.Write([]byte("x")); err != nil {
					t.Fatal(err)
				}
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := inspectState(t.Context(), bytes.NewReader(out.Bytes()), boot, nil); err == nil {
				t.Fatal("accepted invalid rule inventory")
			}
		})
	}
}

func pathForAuditTest(file audit.RuleFile) string { return "audit-rules/" + auditArchiveFile(file) }
