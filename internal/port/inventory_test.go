package port

import (
	"strings"
	"testing"
)

func TestForwardInventoryRetainsBothTransportsAndKnownInactiveUnits(t *testing.T) {
	const legacy = "yoloctl-port-web-8080-80.service"
	const native = "yoloctl-openshell-port-web-8080-80.service"
	got, err := parseForwardInventory(legacy+" loaded failed failed old-route\n"+native+" loaded activating auto-restart new-route\n", "web")
	if err != nil || len(got) != 2 {
		t.Fatalf("complete mixed routes: %+v %v", got, err)
	}
	if got[0].Unit != native || !got[0].OpenShell || got[1].Unit != legacy || got[1].OpenShell || got[0].Spec.HostPort != 8080 || got[1].Spec.HostPort != 8080 {
		t.Fatal("actual transport lost or conflicting host port was deduplicated", got)
	}
	other, err := parseForwardInventory(legacy+"\n"+native+"\n", "another")
	if err != nil || len(other) != 0 {
		t.Fatal("box filter failed", other, err)
	}
}

func TestForwardInventoryRefusesUncertainOwnedNames(t *testing.T) {
	const native = "yoloctl-openshell-port-web-8080-80.service"
	for _, raw := range []string{
		native + "\n" + native + "\n",
		"yoloctl-openshell-port-web-08080-80.service\n",
		"yoloctl-port-web-08080-80.service\n",
		"yoloctl-port-web-8080-80\n",
		"yoloctl-port-web-70000-80.service\n",
		"yoloctl-openshell-port-../web-8080-80.service\n",
		native + "\nyoloctl-port-other-8080-invalid.service\n",
		strings.Repeat("x", (1<<20)+1),
	} {
		if _, err := parseForwardInventory(raw, "web"); err == nil {
			t.Fatal("uncertain global inventory silently treated as complete")
		}
	}
	if got, err := parseForwardInventory("yoloctl-session-unrelated.service\n", ""); err != nil || len(got) != 0 {
		t.Fatal("unrelated infrastructure was interpreted as a port", got, err)
	}
}
