package port

import (
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/container"
)

func TestBoundedNativeForwardInventoryRetainsStrictGlobalParsing(t *testing.T) {
	const native = "yoloctl-openshell-port-web-8080-80.service"
	const legacy = "yoloctl-port-web-8081-81.service"
	for _, test := range []struct {
		name, raw string
		want      int
		failed    bool
	}{
		{"both-retained", native + " loaded failed failed retained\n" + legacy + " loaded inactive dead retained\n", 2, false},
		{"duplicate", native + "\n" + native + "\n", 0, true},
		{"malformed-peer", native + "\nyoloctl-port-other-08080-80.service\n", 0, true},
		{"oversized", strings.Repeat(native+" loaded failed failed retained\n", 20000), 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := forwardCommandFixture(t, test.raw, nil)
			got, err := InventoryBounded(t.Context(), container.New("fixture"), "web")
			if (err != nil) != test.failed || len(got) != test.want {
				t.Fatal("bounded transport bypassed strict/complete identity parsing", got, err)
			}
			commands := forwardCommands(t, dir)
			if !strings.Contains(commands, "list-units --all ") || !strings.Contains(commands, "--full ") || strings.Contains(commands, "systemctl stop") || strings.Contains(commands, "nerdctl") {
				t.Fatal("inventory was incomplete or performed a box effect", commands)
			}
		})
	}
}
