package assets

import (
	"encoding/json"
	"testing"
)

func TestVMRecipeDoesNotDownloadRetiredOpenShell(t *testing.T) {
	raw, err := VMImageRecipe.ReadFile("vm/firecracker/pins.json")
	if err != nil {
		t.Fatal(err)
	}
	var pins map[string]json.RawMessage
	if err := json.Unmarshal(raw, &pins); err != nil {
		t.Fatal(err)
	}
	if _, retired := pins["openshell.tgz"]; retired {
		t.Fatal("VM recipe still downloads the retired runtime")
	}
	for _, required := range []string{"nerdctl.tgz", "firecracker.tgz", "ubuntu.squashfs", "nftables.deb"} {
		if len(pins[required]) == 0 {
			t.Fatal("infrastructure pin lost", required)
		}
	}
}
