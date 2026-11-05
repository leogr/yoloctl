package spaceconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLimaPlacementChecksAllExistingGrants(t *testing.T) {
	for _, writable := range []bool{false, true} {
		for _, relation := range []string{"ancestor", "exact", "descendant", "sibling"} {
			t.Run(relation+map[bool]string{false: "/read-only", true: "/writable"}[writable], func(t *testing.T) {
				root := t.TempDir()
				t.Setenv("YOLOCTL_HOME", filepath.Join(root, "legacy-config"))
				home := filepath.Join(root, "new-home")
				grant := map[string]string{
					"ancestor": root, "exact": home,
					"descendant": filepath.Join(home, "project"), "sibling": home + "-project",
				}[relation]
				c, err := New("old", grant, "personal")
				if err != nil {
					t.Fatal(err)
				}
				c.AccountDiskUUID = "original-uuid"
				boot, _, err := Split(c, 1000, 20, "original-generation")
				if err != nil {
					t.Fatal(err)
				}
				boot.Mounts[0].Writable = writable
				if err := boot.Save(); err != nil {
					t.Fatal(err)
				}
				err = ValidateLimaPlacement(home)
				if relation == "sibling" {
					if err != nil {
						t.Fatal("unrelated grant blocked placement", err)
					}
				} else if err == nil || !strings.Contains(err.Error(), grant) || !strings.Contains(err.Error(), "\"old\"") {
					t.Fatalf("missing conflict detail: %v", err)
				}
				if _, err := os.Stat(home); !os.IsNotExist(err) {
					t.Fatalf("validation created home: %v", err)
				}
			})
		}
	}
}

func TestLimaPlacementResolvesExistingAliasesAndMissingSuffixes(t *testing.T) {
	root := t.TempDir()
	t.Setenv("YOLOCTL_HOME", filepath.Join(root, "legacy"))
	shared := filepath.Join(root, "shared")
	if err := os.Mkdir(shared, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(shared, alias); err != nil {
		t.Fatal(err)
	}
	c, _ := New("old", shared, "personal")
	c.AccountDiskUUID = "original-uuid"
	boot, _, err := Split(c, 1000, 20, "generation")
	if err != nil {
		t.Fatal(err)
	}
	if err := boot.Save(); err != nil {
		t.Fatal(err)
	}
	if err := ValidateLimaPlacement(filepath.Join(alias, "missing", "lima")); err == nil {
		t.Fatal("aliased storage escaped an existing grant")
	}
	boot.Mounts[0].Source = alias
	if err := boot.Save(); err != nil {
		t.Fatal(err)
	}
	if err := ValidateLimaPlacement(filepath.Join(shared, "missing", "lima")); err == nil {
		t.Fatal("aliased grant hid exposed storage")
	}
	dangling := filepath.Join(root, "dangling")
	if err := os.Symlink(filepath.Join(root, "absent"), dangling); err != nil {
		t.Fatal(err)
	}
	if err := ValidateLimaPlacement(filepath.Join(dangling, "lima")); err == nil {
		t.Fatal("dangling link treated as an available directory")
	}
}

func TestLimaPlacementRefusesUncertainGrantInventory(t *testing.T) {
	root := t.TempDir()
	t.Setenv("YOLOCTL_HOME", root)
	for _, raw := range []string{"schema_version: 2\n", "schema_version: 3\nunknown: broken\n"} {
		dir := filepath.Join(root, "spaces", "old")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "space.yaml"), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if err := ValidateLimaPlacement(filepath.Join(root, "new-home")); err == nil || !strings.Contains(err.Error(), "\"old\"") {
			t.Fatalf("uncertain inventory accepted: %v", err)
		}
	}
}

func TestLimaPlacementIncludesHistoricalGrantsWithoutAdmittingLegacyBoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("YOLOCTL_HOME", filepath.Join(root, "legacy"))
	shared := filepath.Join(root, "shared")
	c, err := New("old", shared, "personal")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if err := ValidateLimaPlacement(filepath.Join(shared, "lima")); err == nil {
		t.Fatal("historical grant ignored")
	}
	if err := ValidateLimaPlacement(shared + "-other"); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBootstrap("old"); !errors.Is(err, ErrLegacySchema) {
		t.Fatalf("schema-2 boot admission changed: %v", err)
	}
}
