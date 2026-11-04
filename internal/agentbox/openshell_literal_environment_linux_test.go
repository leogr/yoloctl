package agentbox

import (
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/managedchat"
)

// Reproduce the real chat enrollment fixture: --env QA_NATIVE_ENV=1 must
// survive creation/recreation alongside the chat child-environment envelope.
func TestNativeLiteralEnvironmentSurvivesChatBootstrap(t *testing.T) {
	for _, chat := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "chat"}[chat], func(t *testing.T) {
			c, current, release := nativeContainerPlanFixture(t)
			c.Env = map[string]EnvVar{
				"QA_NATIVE_ENV":             {Literal: "1"},
				"EMPTY":                     {Literal: ""},
				"EQUALS":                    {Literal: "a=b"},
				"OPENSHELL_CONTROL_FIXTURE": {Literal: "must-not-propagate"},
			}
			policy, err := ComposePolicy(c)
			if err != nil {
				t.Fatal(err)
			}
			options, plan, err := planNativeContainer(c, c.Image, current, release, strings.Repeat("d", 32), policy)
			if err != nil {
				t.Fatal(err)
			}
			// Later declaration edits cannot alter this particular create's map.
			c.Env["QA_NATIVE_ENV"] = EnvVar{Literal: "later"}
			if chat {
				c.chat = &managedchat.Projection{RoomID: NewIdentity(), MemberID: NewIdentity()}
				if err := addChatEnvironment(c, &options, c.chat); err != nil {
					t.Fatal(err)
				}
				if err := plan.retainChatEnvironment(c); err != nil {
					t.Fatal(err)
				}
			}
			details := createdNativePlanDetails(options)
			input, err := plan.bootstrapForCreated(details)
			if err != nil {
				t.Fatal(err)
			}
			if input.Environment["QA_NATIVE_ENV"] != "1" || input.Environment["EQUALS"] != "a=b" {
				t.Fatal("declared literal did not reach native workload bootstrap")
			}
			if value, ok := input.Environment["EMPTY"]; !ok || value != "" {
				t.Fatal("explicit empty literal was lost")
			}
			if _, ok := input.Environment["OPENSHELL_CONTROL_FIXTURE"]; ok {
				t.Fatal("runtime control literal crossed the workload boundary")
			}
			if _, ok := input.Environment["PATH"]; ok {
				t.Fatal("unrelated image environment entered private override map")
			}
			if chat && (input.Environment["YOLOCTL_CHAT_CONFIG"] != managedchat.IdentityMount+"/client.toml" || input.Environment["YOLOCTL_CHAT_ROOM"] != c.chat.RoomID) {
				t.Fatal("chat discovery was lost while retaining literals")
			}
			// Bootstrap owns its copy; neither publication nor a caller may
			// change a subsequent read of the retained create plan.
			input.Environment["QA_NATIVE_ENV"] = "changed-result"
			again, err := plan.bootstrapForCreated(details)
			if err != nil || again.Environment["QA_NATIVE_ENV"] != "1" {
				t.Fatal("bootstrap map aliases the retained literal snapshot", err)
			}
		})
	}
}

func TestNativeLiteralEnvironmentPreservesExplicitEnvelopePrecedence(t *testing.T) {
	c, current, release := nativeContainerPlanFixture(t)
	c.Env = map[string]EnvVar{
		"KEY":                    {Literal: "outer-literal"},
		"OTHER":                  {Literal: "retained"},
		openshellUserEnvironment: {Literal: `{"KEY":"explicit-child-override"}`},
	}
	policy, err := ComposePolicy(c)
	if err != nil {
		t.Fatal(err)
	}
	options, plan, err := planNativeContainer(c, c.Image, current, release, strings.Repeat("d", 32), policy)
	if err != nil {
		t.Fatal(err)
	}
	input, err := plan.bootstrapForCreated(createdNativePlanDetails(options))
	if err != nil || input.Environment["KEY"] != "explicit-child-override" || input.Environment["OTHER"] != "retained" {
		t.Fatal("literal/envelope precedence changed", err)
	}
	if _, ok := input.Environment[openshellUserEnvironment]; ok {
		t.Fatal("envelope was nested into child ENV")
	}
}
