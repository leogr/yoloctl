package agentbox

import (
	"context"
	"maps"
	"slices"
	"testing"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/managedchat"
)

func chatRuntimeFixture() (*Config, *container.Details) {
	p := &managedchat.Projection{RoomID: NewIdentity(), BoxID: NewIdentity(), MemberID: NewIdentity(), Binds: []managedchat.Bind{
		{Source: "/native/member", Target: managedchat.IdentityMount, ReadOnly: true},
		{Source: "/run/controller/endpoint", Target: managedchat.EndpointMount, ReadOnly: true},
	}}
	c := &Config{Name: "coder", Space: "work", ID: p.BoxID, chat: p}
	d := &container.Details{ID: "actual-container-id", Name: c.Name, Labels: map[string]string{
		LabelSpace: c.Space, LabelAgentbox: c.Name, managedchat.RoomLabel: p.RoomID, managedchat.BoxLabel: p.BoxID,
		managedchat.MemberLabel: p.MemberID, managedchat.RoleLabel: "member", managedchat.IncarnationLabel: NewIdentity(),
	}}
	for _, b := range p.Binds {
		d.Mounts = append(d.Mounts, container.DetailMount{Source: b.Source, Destination: b.Target, ReadOnly: b.ReadOnly})
	}
	return c, d
}

func TestChatRuntimeRequiresExactIdentityAndPrivateBinds(t *testing.T) {
	c, d := chatRuntimeFixture()
	if err := verifyChatRuntimeProjection(c, d); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*container.Details){
		"wrong-room":      func(d *container.Details) { d.Labels[managedchat.RoomLabel] = NewIdentity() },
		"wrong-box":       func(d *container.Details) { d.Labels[managedchat.BoxLabel] = NewIdentity() },
		"wrong-member":    func(d *container.Details) { d.Labels[managedchat.MemberLabel] = NewIdentity() },
		"wrong-scope":     func(d *container.Details) { d.Labels[LabelSpace] = "other" },
		"no-incarnation":  func(d *container.Details) { delete(d.Labels, managedchat.IncarnationLabel) },
		"no-container-id": func(d *container.Details) { d.ID = "" },
		"missing-bind":    func(d *container.Details) { d.Mounts = d.Mounts[1:] },
		"duplicate-bind":  func(d *container.Details) { d.Mounts = append(d.Mounts, d.Mounts[0]) },
		"writable-token":  func(d *container.Details) { d.Mounts[0].ReadOnly = false },
		"foreign-leaf":    func(d *container.Details) { d.Mounts[0].Source = "/native/other-member" },
		"control-exposed": func(d *container.Details) {
			d.Mounts = append(d.Mounts, container.DetailMount{Source: "/run/controller/control", Destination: managedchat.ControlMount})
		},
		"parent-overlay": func(d *container.Details) {
			d.Mounts = append(d.Mounts, container.DetailMount{Source: "/foreign", Destination: "/run"})
		},
		"child-overlay": func(d *container.Details) {
			d.Mounts = append(d.Mounts, container.DetailMount{Source: "/foreign", Destination: managedchat.IdentityMount + "/token"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			copy := *d
			copy.Labels = maps.Clone(d.Labels)
			copy.Mounts = slices.Clone(d.Mounts)
			mutate(&copy)
			if err := verifyChatRuntimeProjection(c, &copy); err == nil {
				t.Fatal("invalid authority accepted")
			}
		})
	}
	c.chat = nil
	if err := verifyChatRuntimeProjection(c, d); err == nil {
		t.Fatal("revoked container accepted")
	}
}

func TestChatBindingChangeRequiresRecreateAndContextIsExplicit(t *testing.T) {
	c, d := chatRuntimeFixture()
	a := ActualState{Exists: true, Labels: d.Labels}
	if reason := chatBindingReason(c, a); reason == "" {
		t.Fatal("missing native binds ignored")
	}
	a.ChatMounts = chatMounts(c.chat)
	if reason := chatBindingReason(c, a); reason != "" {
		t.Fatal(reason)
	}
	c.chat = nil
	if reason := chatBindingReason(c, a); reason == "" {
		t.Fatal("revocation ignored")
	}
	ctx := context.Background()
	if ChatRecreateAllowed(ctx) || !ChatRecreateAllowed(WithChatRecreate(ctx)) {
		t.Fatal("implicit recreation authority")
	}
}

func TestChatInfrastructureCannotBecomeOrdinaryMount(t *testing.T) {
	for _, target := range []string{managedchat.IdentityMount, managedchat.EndpointMount, managedchat.ControlMount, managedchat.HumanMount, managedchat.ServerDataMount} {
		if err := ValidateChatMountTargets(&Config{Mounts: []Mount{{Source: "/host", Target: target}}}); err == nil {
			t.Fatal("ordinary chat bind accepted", target)
		}
	}
}
