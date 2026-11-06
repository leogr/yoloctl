//go:build linux

package agentbox

import (
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/managedchat"
)

func TestAWSMaterialSurvivesNativeAndChatBootstrapWithoutPublicExposure(t *testing.T) {
	keys := []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_BEARER_TOKEN_BEDROCK"}
	for _, agent := range []string{"claude", "codex"} {
		for _, chat := range []bool{false, true} {
			name := agent + "/ordinary"
			if chat {
				name = agent + "/chat"
			}
			t.Run(name, func(t *testing.T) {
				c, current, release := nativeContainerPlanFixture(t)
				c.Agent = agent
				c.Account = "aws-account"
				acct := account.New(c.Space, c.Account)
				for _, key := range keys {
					acct.SetSecret(key, "synthetic-private-"+key)
				}
				acct.SetSecret("AWS_UNKNOWN", "synthetic-private-unknown")
				if err := acct.Save(); err != nil {
					t.Fatal(err)
				}
				c.Env = map[string]EnvVar{"AWS_REGION": {Literal: "us-east-1"}, "AWS_ACCESS_KEY_ID": {Literal: "literal-must-not-win"}}
				if agent == "claude" {
					c.Env["CLAUDE_CODE_USE_BEDROCK"] = EnvVar{Literal: "1"}
				}
				policy, err := ComposePolicy(c)
				if err != nil {
					t.Fatal(err)
				}
				options, plan, err := planNativeContainer(c, c.Image, current, release, strings.Repeat("d", 32), policy)
				if err != nil {
					t.Fatal(err)
				}
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
				for _, key := range keys {
					if input.Environment[key] != "synthetic-private-"+key {
						t.Fatalf("declared key %s lost or precedence changed", key)
					}
				}
				if _, ok := input.Environment["AWS_UNKNOWN"]; ok {
					t.Fatal("wildcard AWS projection")
				}
				if input.Environment["AWS_REGION"] != "us-east-1" {
					t.Fatal("provider region lost")
				}
				if agent == "claude" && input.Environment["CLAUDE_CODE_USE_BEDROCK"] != "1" {
					t.Fatal("explicit provider flag lost")
				}
				if strings.Contains(strings.Join(options.Command, " ")+strings.Join(options.Env, " "), "synthetic-private") {
					t.Fatal("secret in launch argv")
				}
				for _, value := range options.Labels {
					if strings.Contains(value, "synthetic-private") {
						t.Fatal("secret in label")
					}
				}
				// A saved replacement affects a new create, not the retained old plan.
				acct.SetSecret("AWS_ACCESS_KEY_ID", "synthetic-private-replacement")
				if err := acct.Save(); err != nil {
					t.Fatal(err)
				}
				old, err := plan.bootstrapForCreated(details)
				if err != nil || old.Environment["AWS_ACCESS_KEY_ID"] != "synthetic-private-AWS_ACCESS_KEY_ID" {
					t.Fatal("running creation changed without recreation")
				}
				nextOptions, nextPlan, err := planNativeContainer(c, c.Image, current, release, strings.Repeat("e", 32), policy)
				if err != nil {
					t.Fatal(err)
				}
				next, err := nextPlan.bootstrapForCreated(createdNativePlanDetails(nextOptions))
				if err != nil || next.Environment["AWS_ACCESS_KEY_ID"] != "synthetic-private-replacement" {
					t.Fatal("recreate did not reload stored secrets", err)
				}
			})
		}
	}
}

func TestAWSMaterialProjectionIsNarrowAndMissingKeysDoNotBlockLaunch(t *testing.T) {
	for _, agent := range []string{"claude", "codex", "gemini"} {
		for _, partial := range []bool{false, true} {
			t.Run(agent+map[bool]string{false: "/absent", true: "/partial"}[partial], func(t *testing.T) {
				c, current, release := nativeContainerPlanFixture(t)
				c.Agent = agent
				c.Account = "aws-account"
				acct := account.New(c.Space, c.Account)
				if partial {
					acct.SetSecret("AWS_ACCESS_KEY_ID", "synthetic-private-partial")
				}
				acct.SetSecret("AWS_UNKNOWN", "synthetic-private-unknown")
				if err := acct.Save(); err != nil {
					t.Fatal(err)
				}
				policy, err := ComposePolicy(c)
				if err != nil {
					t.Fatal(err)
				}
				options, plan, err := planNativeContainer(c, c.Image, current, release, strings.Repeat("d", 32), policy)
				if err != nil {
					t.Fatal("missing credentials blocked launch", err)
				}
				input, err := plan.bootstrapForCreated(createdNativePlanDetails(options))
				if err != nil {
					t.Fatal(err)
				}
				_, hasAccess := input.Environment["AWS_ACCESS_KEY_ID"]
				if hasAccess != (partial && agent != "gemini") {
					t.Fatal("undeclared/missing AWS key handling changed")
				}
				if _, ok := input.Environment["AWS_UNKNOWN"]; ok {
					t.Fatal("unknown AWS key projected")
				}
			})
		}
	}
}
