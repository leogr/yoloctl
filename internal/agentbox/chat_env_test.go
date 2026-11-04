package agentbox

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentboximage"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/managedchat"
)

func TestChatDiscoveryReachesConfinedChildAndPreservesAccountInjection(t *testing.T) {
	c := &Config{}
	p := &managedchat.Projection{RoomID: NewIdentity(), MemberID: NewIdentity()}
	opts := container.RunOptions{Env: []string{openshellUserEnvironment + `={"AGENT_KEY":"private-fixture"}`, "YOLOCTL_HOST_UID=1000"}}
	if err := addChatEnvironment(c, &opts, p); err != nil {
		t.Fatal(err)
	}
	var env map[string]string
	if err := json.Unmarshal([]byte(strings.TrimPrefix(opts.Env[0], openshellUserEnvironment+"=")), &env); err != nil {
		t.Fatal(err)
	}
	if env["AGENT_KEY"] != "private-fixture" || env["YOLOCTL_CHAT_CONFIG"] != managedchat.IdentityMount+"/client.toml" || env["YOLOCTL_CHAT_ROOM"] != p.RoomID || len(opts.Env) != 2 {
		t.Fatal("missing confined discovery or changed account injection")
	}
	// Neither declaration env nor a secret-injection alias can replace the
	// controller's discovery identity or redirect it to another leaf.
	c.Env = map[string]EnvVar{"YOLOCTL_CHAT_CONFIG": {Literal: "/elsewhere"}}
	if addChatEnvironment(c, &container.RunOptions{}, p) == nil {
		t.Fatal("declaration overrode chat discovery")
	}
	c.Env = nil
	bad := container.RunOptions{Env: []string{openshellUserEnvironment + `={"YOLOCTL_CHAT_ROOM":"private-fixture"}`}}
	if err := addChatEnvironment(c, &bad, p); err == nil || strings.Contains(err.Error(), "private-fixture") {
		t.Fatal("injection override accepted or disclosed")
	}
}

func TestChatServerProjectionCannotBecomeAnotherWorkload(t *testing.T) {
	reserved := managedchat.Box{Name: "chat", ID: NewIdentity()}
	c := Config{Name: reserved.Name, ID: reserved.ID, Space: "work", Agent: "none", Account: "none", Image: agentboximage.RefAgentbox, Workdir: NewEphemeralWorkdir("chat"), Features: []string{"yolo.chat.skill"}, SessionCommand: ChatServerCommand()}
	if err := ValidateChatServer(&c, reserved); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Config){
		"foreign image":        func(c *Config) { c.Image = "unrelated/server:1" },
		"agent":                func(c *Config) { c.Agent = "codex"; c.Account = "shared" },
		"operator":             func(c *Config) { c.Capabilities = []string{"yolo.space.operator"} },
		"different executable": func(c *Config) { c.SessionCommand = []string{"sh"} },
		"host workdir": func(c *Config) {
			c.Workdir = Workdir{Mode: WorkdirModeHost, Source: "/home/project", Canonical: "/home/project"}
		},
		"foreign ID": func(c *Config) { c.ID = NewIdentity() },
	} {
		t.Run(name, func(t *testing.T) {
			copy := c
			change(&copy)
			if ValidateChatServer(&copy, reserved) == nil {
				t.Fatal("server authority granted to a different workload")
			}
		})
	}
}

// serverEnv is a server container's child-environment envelope.
func serverEnv(json string) []string {
	return []string{"PATH=/usr/bin", openshellUserEnvironment + "=" + json}
}

func childEnvironment(t *testing.T, opts container.RunOptions) map[string]string {
	t.Helper()
	var env map[string]string
	for _, kv := range opts.Env {
		if raw, ok := strings.CutPrefix(kv, openshellUserEnvironment+"="); ok {
			if env != nil || json.Unmarshal([]byte(raw), &env) != nil {
				t.Fatalf("invalid child environment: %v", opts.Env)
			}
		}
	}
	return env
}

// Only the managed server's confined children get the writable temporary
// directory, beside its discovery values; members keep their environment.
func TestChatServerChildrenGetAWritableTemporaryDirectory(t *testing.T) {
	server := &managedchat.Projection{Server: true, RoomID: NewIdentity(), MemberID: NewIdentity()}
	opts := container.RunOptions{Env: []string{"YOLOCTL_HOST_UID=1000"}}
	if err := addChatEnvironment(&Config{}, &opts, server); err != nil {
		t.Fatal(err)
	}
	env := childEnvironment(t, opts)
	if env["TMPDIR"] != "/tmp" || env["SQLITE_TMPDIR"] != "/tmp" || env["YOLOCTL_CHAT_CONFIG"] != managedchat.HumanMount+"/client.toml" || env["YOLOCTL_CHAT_ROOM"] != server.RoomID || opts.Env[0] != "YOLOCTL_HOST_UID=1000" {
		t.Fatalf("server child environment: %v", env)
	}
	if reason := chatServerEnvironmentReason(opts.Env); reason != "" {
		t.Fatalf("a server created by this build drifts: %s", reason)
	}
	member := &managedchat.Projection{RoomID: NewIdentity(), MemberID: NewIdentity()}
	opts = container.RunOptions{Env: []string{openshellUserEnvironment + `={"AGENT_KEY":"private-fixture"}`}}
	if err := addChatEnvironment(&Config{}, &opts, member); err != nil {
		t.Fatal(err)
	}
	if env := childEnvironment(t, opts); env["AGENT_KEY"] != "private-fixture" || env["TMPDIR"] != "" || env["SQLITE_TMPDIR"] != "" {
		t.Fatalf("member child environment: %v", env)
	}
	// A conflicting value fails closed without naming it.
	for _, bad := range []struct {
		cfg  *Config
		opts container.RunOptions
	}{
		{&Config{Env: map[string]EnvVar{"SQLITE_TMPDIR": {Literal: "/private-fixture"}}}, container.RunOptions{}},
		{&Config{}, container.RunOptions{Env: []string{openshellUserEnvironment + `={"TMPDIR":"/private-fixture"}`}}},
		{&Config{}, container.RunOptions{Env: []string{openshellUserEnvironment + `={"TMPDIR":null}`}}},
		{&Config{}, container.RunOptions{Env: []string{openshellUserEnvironment + `={}`, openshellUserEnvironment + `={}`}}},
	} {
		if err := addChatEnvironment(bad.cfg, &bad.opts, server); err == nil || strings.Contains(err.Error(), "private-fixture") {
			t.Errorf("conflict accepted or disclosed: %v", err)
		}
	}
}

// A server container lacking the temporary directory, as one created before
// it, is recreation drift; so is any envelope that cannot prove both values.
// No value, including an account secret, ever appears in the reason.
func TestChatServerTemporaryDirectoryDriftRequiresRecreate(t *testing.T) {
	good := `{"SQLITE_TMPDIR":"/tmp","TMPDIR":"/tmp","YOLOCTL_CHAT_ROOM":"r"}`
	for name, c := range map[string]struct {
		env  []string
		want string
	}{
		"created by this build": {serverEnv(good), ""},
		"created before":        {serverEnv(`{"YOLOCTL_CHAT_ROOM":"r","AGENT_KEY":"private-fixture"}`), "lacks its temporary directory (SQLITE_TMPDIR)"},
		"no envelope":           {[]string{"PATH=/usr/bin", "TMPDIR=/tmp"}, "lacks its sandbox child environment"},
		"duplicate envelope":    {append(serverEnv(good), openshellUserEnvironment+"="+good), "more than one sandbox child environment"},
		"malformed":             {serverEnv(`{"TMPDIR":"/tmp"`), "invalid sandbox child environment"},
		"not an object":         {serverEnv(`["private-fixture"]`), "invalid sandbox child environment"},
		"null value":            {serverEnv(`{"SQLITE_TMPDIR":"/tmp","TMPDIR":"/tmp","AGENT_KEY":null}`), "invalid sandbox child environment"},
		"number value":          {serverEnv(`{"SQLITE_TMPDIR":"/tmp","TMPDIR":1}`), "invalid sandbox child environment"},
		"wrong value":           {serverEnv(`{"SQLITE_TMPDIR":"/private-fixture","TMPDIR":"/tmp"}`), "lacks its temporary directory (SQLITE_TMPDIR)"},
		"one value":             {serverEnv(`{"TMPDIR":"/tmp"}`), "lacks its temporary directory (SQLITE_TMPDIR)"},
	} {
		t.Run(name, func(t *testing.T) {
			got := chatServerEnvironmentReason(c.env)
			if c.want == "" && got != "" || c.want != "" && (!strings.Contains(got, c.want) || !strings.Contains(got, "explicit box recreate required")) {
				t.Fatalf("reason %q, want %q", got, c.want)
			}
			if strings.Contains(got, "private-fixture") || strings.Contains(got, "/tmp") {
				t.Fatalf("a value leaked: %q", got)
			}
		})
	}
}

// Diff, through the live inspector's environment, recreates an old server
// container for its temporary directory; an uninspected environment stays
// explicitly unverified, never healthy; a member's is never compared. The
// old server's runtime verification, which gates its connections, still
// passes: this is deployment drift, not authority.
func TestChatServerDiffNamesTemporaryDirectoryRecreation(t *testing.T) {
	c, d := chatRuntimeFixture()
	c.chat.Server = true
	d.Labels[managedchat.RoleLabel] = "server"
	actual := ActualState{Exists: true, Labels: d.Labels, ChatMounts: chatMounts(c.chat), ReadOnlyWD: !c.WorkdirWritable()}
	if plan := Diff(c, actual, "", nil); !plan.NeedsRecreate || !strings.Contains(plan.RecreateReason, "not inspected, so its temporary directory is unverified; inspect the server box again") {
		t.Fatalf("uninspected environment: %+v", plan)
	}
	if reason := chatBindingReason(c, actual); reason != "" {
		t.Fatalf("identity and binds: %q", reason)
	}
	for _, env := range [][]string{nil, serverEnv(`{"YOLOCTL_CHAT_ROOM":"r"}`), serverEnv(`{"TMPDIR":"/elsewhere"}`)} {
		d.Env = env
		if err := verifyChatRuntimeProjection(c, d); err != nil {
			t.Fatalf("an older server stays usable: %v", err)
		}
	}
	actual.Env = serverEnv(`{"YOLOCTL_CHAT_ROOM":"r"}`)
	plan := Diff(c, actual, "", nil)
	if !plan.NeedsRecreate || !strings.Contains(plan.RecreateReason, "temporary directory") {
		t.Fatalf("old server: %+v", plan)
	}
	// Start and restart preserve the container, so they refuse it.
	if err := checkStartPlan(c, plan); err == nil || !strings.Contains(err.Error(), "requires recreation (managed chat server runtime lacks its temporary directory") {
		t.Fatalf("start preflight: %v", err)
	}
	actual.Env = serverEnv(`{"SQLITE_TMPDIR":"/elsewhere","TMPDIR":"/tmp"}`)
	if plan := Diff(c, actual, "", nil); !plan.NeedsRecreate || !strings.Contains(plan.RecreateReason, "temporary directory (SQLITE_TMPDIR)") || strings.Contains(plan.RecreateReason, "/elsewhere") {
		t.Fatalf("wrong value: %+v", plan)
	}
	actual.Env = serverEnv(`{"SQLITE_TMPDIR":"/tmp","TMPDIR":"/tmp"}`)
	if plan := Diff(c, actual, "", nil); plan.NeedsRecreate {
		t.Fatalf("current server: %+v", plan)
	}
	member, md := chatRuntimeFixture()
	for _, env := range [][]string{nil, serverEnv(`{}`)} {
		if plan := Diff(member, ActualState{Exists: true, Labels: md.Labels, ChatMounts: chatMounts(member.chat), ReadOnlyWD: !member.WorkdirWritable(), Env: env}, "", nil); plan.NeedsRecreate {
			t.Fatalf("member: %+v", plan)
		}
	}
}

// The fix leaves the reserved server's launch literal, and so every existing
// declaration's validation, exactly as before.
func TestChatServerCommandIsUnchanged(t *testing.T) {
	want := []string{"/bin/sh", "-c", "umask 077; exec /usr/local/bin/aircd serve --config " + managedchat.ServerConfigMount + "/server.toml"}
	if got := ChatServerCommand(); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("ChatServerCommand = %q", got)
	}
}
