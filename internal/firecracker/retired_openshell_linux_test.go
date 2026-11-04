//go:build linux && amd64

package firecracker

import "testing"

func TestAssemblyDropsOnlyRetiredOpenShellFiles(t *testing.T) {
	tree := imageTree{}
	for _, name := range []string{"opt/openshell", "opt/openshell/bin", "opt/openshell/bin/openshell-sandbox", "etc/yoloctl/sandbox-policy.rego", "opt/openshell-other/keep", "etc/yoloctl/keep", "opt/yoloctl/keep", "var/lib/yoloctl/account-state/keep"} {
		tree.put(name, 0644, []byte("original"))
	}
	tree.dropLegacyOpenShell()
	for _, name := range []string{"opt/openshell", "opt/openshell/bin", "opt/openshell/bin/openshell-sandbox", "etc/yoloctl/sandbox-policy.rego"} {
		if _, ok := tree[name]; ok {
			t.Fatal("retired artifact survived", name)
		}
	}
	for _, name := range []string{"opt/openshell-other/keep", "etc/yoloctl/keep", "opt/yoloctl/keep", "var/lib/yoloctl/account-state/keep"} {
		if _, ok := tree[name]; !ok {
			t.Fatal("unrelated virtual image input removed", name)
		}
	}
	if imageRecipeVersion < 4 {
		t.Fatal("assembly change did not invalidate old automatic cache")
	}
}
