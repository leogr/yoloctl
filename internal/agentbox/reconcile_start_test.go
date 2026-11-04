package agentbox

import (
	"reflect"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/managedchat"
)

func TestReconcileForStartPreservesExistingContainer(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, test := range []struct {
		name   string
		mutate func(*ActualState)
	}{
		{"image", func(a *ActualState) { a.Image = "img:old" }},
		{"memory", func(a *ActualState) { a.Memory = "2147483648" }},
		{"mount inspection regression", func(a *ActualState) { a.WorkdirSource = "/host/other-project" }},
		{"workdir permissions", func(a *ActualState) { a.ReadOnlyWD = true }},
		{"extra mount", func(a *ActualState) { a.Mounts = []Mount{{Source: "/extra", Target: "/extra"}} }},
		{"revoked chat bind", func(a *ActualState) {
			a.ChatMounts = []Mount{{Source: "/native/old-member", Target: managedchat.IdentityMount, ReadOnly: true}}
		}},
		{"retired chat identity", func(a *ActualState) {
			a.Labels = map[string]string{managedchat.RoomLabel: NewIdentity()}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := validConfig()
			state := ActualState{Exists: true, Image: mustResolveImage(t, c), PolicyContent: mustCompose(t, c)}
			test.mutate(&state)
			inspector := &fakeInspector{state: state}
			applier := &fakeApplier{}
			if err := CheckStart(t.Context(), c, inspector); err == nil {
				t.Fatal("preflight accepted recreate-only drift")
			}
			result, err := ReconcileForStart(t.Context(), c, inspector, applier)
			if err == nil || !strings.Contains(err.Error(), "box inspect coder --space default") || !strings.Contains(err.Error(), "box recreate coder --space default") {
				t.Fatalf("missing actionable refusal: %v", err)
			}
			if result.HasChanges() || !reflect.DeepEqual(applier, &fakeApplier{}) {
				t.Fatalf("start changed runtime on refused replacement: result=%+v calls=%+v", result, applier)
			}
			// Explicit reconcile retains its existing replacement semantics.
			result, err = Reconcile(t.Context(), c, inspector, applier)
			if err != nil || !result.ContainerRecreated || len(applier.recreateCalls) != 1 {
				t.Fatalf("explicit reconcile lost replacement: result=%+v error=%v", result, err)
			}
		})
	}
}

func TestReconcileForStartKeepsCreatePolicyAndPortActions(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, action := range []string{"missing", "policy", "ports", "unchanged"} {
		t.Run(action, func(t *testing.T) {
			c := validConfig()
			c.Ports = []Port{{Published: 3000, Target: 3000}}
			state := ActualState{Exists: true, Image: mustResolveImage(t, c), PolicyContent: mustCompose(t, c), PortForwards: c.Ports}
			switch action {
			case "missing":
				state = ActualState{}
			case "policy":
				state.PolicyContent = []byte("old policy")
			case "ports":
				state.PortForwards = []Port{{Published: 4000, Target: 4000}}
			}
			inspector := &fakeInspector{state: state}
			if err := CheckStart(t.Context(), c, inspector); err != nil {
				t.Fatal(err)
			}
			applier := &fakeApplier{}
			result, err := ReconcileForStart(t.Context(), c, inspector, applier)
			if err != nil || result.ContainerRecreated || len(applier.recreateCalls) != 0 {
				t.Fatalf("result=%+v error=%v", result, err)
			}
			switch action {
			case "missing":
				if !result.ContainerCreated || len(applier.createCalls) != 1 || !reflect.DeepEqual(result.PortForwardsAdded, c.Ports) {
					t.Fatalf("missing box was not materialized with its ports: %+v", result)
				}
			case "policy":
				if !result.ContainerRestarted || len(applier.restartCalls) != 1 || !reflect.DeepEqual(result.PortForwardsAdded, c.Ports) {
					t.Fatalf("policy restart or ports lost: %+v", result)
				}
			case "ports":
				if len(applier.addCalls) != 1 || len(applier.removeCalls) != 1 || applier.addCalls[0].pf != c.Ports[0] || applier.removeCalls[0].pf != state.PortForwards[0] {
					t.Fatalf("port diff lost: %+v", applier)
				}
			case "unchanged":
				if result.HasChanges() || !reflect.DeepEqual(applier, &fakeApplier{}) {
					t.Fatalf("unchanged box mutated: %+v", applier)
				}
			}
		})
	}
}

func TestReconcileForStartRechecksAfterPreflight(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c := validConfig()
	inspector := &fakeInspector{state: ActualState{Exists: true, Image: mustResolveImage(t, c), PolicyContent: mustCompose(t, c)}}
	if err := CheckStart(t.Context(), c, inspector); err != nil {
		t.Fatal(err)
	}
	inspector.state.Image = "img:changed-after-preflight"
	applier := &fakeApplier{}
	_, err := ReconcileForStart(t.Context(), c, inspector, applier)
	if err == nil || !reflect.DeepEqual(applier, &fakeApplier{}) {
		t.Fatalf("late drift escaped preservation guard: error=%v calls=%+v", err, applier)
	}
}
