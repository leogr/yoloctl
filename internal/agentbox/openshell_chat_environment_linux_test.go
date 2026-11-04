package agentbox

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/managedchat"
)

func TestNativeChatAccountInjectionRetainsDiscoveryPrivately(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c, current, release := nativeContainerPlanFixture(t)
	c.Agent, c.Account = "claude", account.Default
	c.Env = map[string]EnvVar{openshellUserEnvironment: {Literal: `{"LITERAL_ONLY":"not-selected"}`}}
	acct := account.New(c.Space, c.Account)
	acct.SetSecret("ANTHROPIC_API_KEY", "private-account-fixture")
	if err := acct.Save(); err != nil {
		t.Fatal(err)
	}
	policy, err := ComposePolicy(c)
	if err != nil {
		t.Fatal(err)
	}
	options, plan, err := planNativeContainer(c, c.Image, current, release, strings.Repeat("d", 32), policy)
	if err != nil {
		t.Fatal(err)
	}
	c.chat = &managedchat.Projection{RoomID: NewIdentity(), MemberID: NewIdentity()}
	if err := addChatEnvironment(c, &options, c.chat); err != nil {
		t.Fatal(err)
	}
	if err := plan.retainChatEnvironment(c); err != nil {
		t.Fatal(err)
	}
	input, err := plan.bootstrapForCreated(createdNativePlanDetails(options))
	if err != nil {
		t.Fatal(err)
	}
	public := childEnvironment(t, options)
	for key, want := range public {
		if key == "LITERAL_ONLY" {
			continue // account overrides still replace arbitrary literal/image maps
		}
		if input.Environment[key] != want {
			t.Fatalf("missing controller chat key %s", key)
		}
	}
	if input.Environment["ANTHROPIC_API_KEY"] != "private-account-fixture" || len(input.Environment) != 4 {
		t.Fatal("account channel lost or literal/image map broadened")
	}
	for _, exposed := range []string{strings.Join(options.Env, "\n"), strings.Join(options.Command, "\n"), fmt.Sprint(options.Labels), fmt.Sprintf("%v %#v", plan, plan)} {
		if strings.Contains(exposed, "private-account-fixture") {
			t.Fatal("account value escaped its private bootstrap")
		}
	}
}

func TestNativeChatEnvironmentKeepsNonInjectedAndDormantSelection(t *testing.T) {
	for _, server := range []bool{false, true} {
		c := &Config{chat: &managedchat.Projection{Server: server, RoomID: NewIdentity(), MemberID: NewIdentity()}}
		var options container.RunOptions
		if err := addChatEnvironment(c, &options, c.chat); err != nil {
			t.Fatal(err)
		}
		plan := nativeContainerPlan{}
		before := strings.Join(options.Env, "\n")
		if err := plan.retainChatEnvironment(c); err != nil || plan.privateEnv != "" || strings.Join(options.Env, "\n") != before {
			t.Fatal("noninjected inspected-envelope fallback changed", err)
		}
		if server && chatServerEnvironmentReason(options.Env) != "" {
			t.Fatal("server temporary-directory projection lost")
		}
	}
	plan := nativeContainerPlan{privateEnv: `{"AGENT_KEY":"private"}`}
	if err := plan.retainChatEnvironment(&Config{}); err != nil || plan.privateEnv != `{"AGENT_KEY":"private"}` {
		t.Fatal("dormant chat changed private account selection", err)
	}
}

func TestNativeChatEnvironmentRefusesReservedAccountCollisionWithoutDisclosure(t *testing.T) {
	for _, key := range []string{"YOLOCTL_CHAT_CONFIG", "YOLOCTL_CHAT_ROOM", "YOLOCTL_CHAT_ACCOUNT", "TMPDIR", "SQLITE_TMPDIR"} {
		t.Run(key, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]string{key: "private-collision-fixture"})
			plan := nativeContainerPlan{privateEnv: string(raw)}
			before := plan.privateEnv
			c := &Config{chat: &managedchat.Projection{Server: true, RoomID: NewIdentity(), MemberID: NewIdentity()}}
			err := plan.retainChatEnvironment(c)
			if err == nil || strings.Contains(err.Error(), "private-collision-fixture") || plan.privateEnv != before {
				t.Fatal("collision accepted/disclosed or partial bootstrap retained", err)
			}
		})
	}
	for _, raw := range []string{"null", `{"KEY":null}`, "{", strings.Repeat("x", (1<<20)+1)} {
		if _, err := nativeChatEnvironmentMap(raw); err == nil {
			t.Fatal("invalid private map accepted")
		}
	}
}

func TestNativeChatEnvironmentRetainsOnlyProjectedPublicKeys(t *testing.T) {
	plan := nativeContainerPlan{privateEnv: `{"KEY":"private"}`}
	c := &Config{chat: &managedchat.Projection{Server: true, RoomID: NewIdentity(), MemberID: NewIdentity()}}
	if err := plan.retainChatEnvironment(c); err != nil {
		t.Fatal(err)
	}
	got, err := nativeChatEnvironmentMap(plan.privateEnv)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"KEY": "private", "YOLOCTL_CHAT_CONFIG": managedchat.HumanMount + "/client.toml", "YOLOCTL_CHAT_ROOM": c.chat.RoomID, "TMPDIR": "/tmp", "SQLITE_TMPDIR": "/tmp"}
	want["YOLOCTL_CHAT_ACCOUNT"], _ = managedchat.AuthAccount(c.chat.MemberID)
	if !maps.Equal(got, want) {
		t.Fatal("private/public projection differs")
	}
}
