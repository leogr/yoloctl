package port

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/container"
)

func TestLegacyDNATUsesExactSelectedIDAndNeverFallsBackToName(t *testing.T) {
	for _, remove := range []bool{false, true} {
		for _, state := range []string{"valid", "absent", "short-id", "inspect-error", "exec-error"} {
			t.Run(state+"/remove="+map[bool]string{true: "yes", false: "no"}[remove], func(t *testing.T) {
				d := legacyForwardDetails()
				d.Name = "c1"
				d.Labels["yoloctl.agentbox"] = "c1"
				dir := forwardCommandFixture(t, "", d)
				raw, err := os.ReadFile(filepath.Join(dir, "inspect.json"))
				if err != nil {
					t.Fatal(err)
				}
				var entries []map[string]any
				if err := json.Unmarshal(raw, &entries); err != nil {
					t.Fatal(err)
				}
				entries = append([]map[string]any{{"Id": "c1" + strings.Repeat("0", 62), "Name": "peer"}}, entries...)
				switch state {
				case "absent":
					entries = entries[:1]
				case "short-id":
					entries[1]["Id"] = "bad"
				case "inspect-error":
					t.Setenv("YOLOCTL_TEST_FORWARD_INSPECT_EXIT", "19")
				case "exec-error":
					t.Setenv("YOLOCTL_TEST_FORWARD_EXEC_EXIT", "21")
				}
				raw, err = json.Marshal(entries)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "inspect.json"), raw, 0o600); err != nil {
					t.Fatal(err)
				}
				c := container.New("fixture")
				if remove {
					removeInnerNetnsDNAT(t.Context(), c, "c1", 80)
				} else if err := installInnerNetnsDNAT(t.Context(), c, "c1", 80); (err == nil) != (state == "valid") {
					t.Fatal("install failure lost", err)
				}
				calls := forwardCommands(t, dir)
				if strings.Count(calls, "nerdctl inspect c1\n") != 1 {
					t.Fatal("lookup missing or repeated", calls)
				}
				if state == "valid" || state == "exec-error" {
					op := "-A"
					if remove {
						op = "-D"
					}
					if strings.Count(calls, "nerdctl exec "+d.ID+" iptables -t nat "+op+" ") != 1 || strings.Count(calls, "\n") != 2 {
						t.Fatal("wrong target, repeated exec or wrong rule", calls)
					}
				} else if strings.Contains(calls, "nerdctl exec") {
					t.Fatal("unresolved identity reached DNAT exec", calls)
				}
			})
		}
	}
}

func TestForwardKeepsBridgeAndExecOnTheSameInspectedContainer(t *testing.T) {
	for _, direct := range []bool{false, true} {
		t.Run(map[bool]string{true: "direct", false: "legacy"}[direct], func(t *testing.T) {
			d := legacyForwardDetails()
			d.Name = "c1"
			dir := forwardCommandFixture(t, "", d)
			raw := `[{"Id":"c1` + strings.Repeat("0", 62) + `","Name":"peer","NetworkSettings":{"IPAddress":"10.4.0.99"}},{"Id":"` + d.ID + `","Name":"c1","NetworkSettings":{"IPAddress":"10.4.0.7"}}]`
			if err := os.WriteFile(filepath.Join(dir, "inspect.json"), []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			err := Forward(t.Context(), container.New("fixture"), Spec{Agentbox: "c1", HostPort: 8080, ContainerPort: 80, Direct: direct})
			if err != nil {
				t.Fatal(err)
			}
			calls := forwardCommands(t, dir)
			if strings.Count(calls, "nerdctl inspect c1\n") != 1 || !strings.Contains(calls, "TCP:10.4.0.7:80") || strings.Contains(calls, "10.4.0.99") {
				t.Fatal("bridge target changed or was inspected twice", calls)
			}
			if strings.Contains(calls, "nerdctl exec "+d.ID+" iptables") == direct || strings.Contains(calls, "nerdctl exec c1 ") {
				t.Fatal("DNAT target/mode changed", calls)
			}
		})
	}
}
