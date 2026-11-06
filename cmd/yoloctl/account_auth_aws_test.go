package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/accountstate"
	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/capabilities"
	"github.com/leogr/yoloctl/internal/layout"
)

func TestBedrockDocumentationUsesRealFlagsAndCompilesCustomEgress(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	doc, err := os.ReadFile("../../docs/digests/bedrock.md")
	if err != nil {
		t.Fatal(err)
	}
	_, after, found := strings.Cut(string(doc), "```yaml\n")
	if !found {
		t.Fatal("missing documented capability")
	}
	capability, _, found := strings.Cut(after, "```")
	if !found {
		t.Fatal("unterminated capability fixture")
	}
	dir, err := layout.CapabilitiesDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "network.my-bedrock.yaml"), []byte(capability), 0o600); err != nil {
		t.Fatal(err)
	}
	custom, err := capabilities.Lookup("network.my-bedrock")
	if err != nil || len(custom.Network) != 1 {
		t.Fatal("documented custom capability rejected", err)
	}
	if capabilities.IsBuiltin("network.bedrock") {
		t.Fatal("Bedrock was added as a builtin capability")
	}
	commands := 0
	for _, line := range strings.Split(string(doc), "\n") {
		args, ok := strings.CutPrefix(line, "yoloctl box create ")
		if !ok {
			continue
		}
		cmd := newAgentboxCreateCmd()
		if err := cmd.Flags().Parse(strings.Fields(args)); err != nil {
			t.Fatal("documented create flags rejected", err)
		}
		agent, _ := cmd.Flags().GetString("agent")
		cfg := &agentbox.Config{Agent: agent, Features: []string{"aws"}, Capabilities: []string{custom.Name}}
		policy, err := agentbox.ComposePolicy(cfg)
		if err != nil || !strings.Contains(string(policy), "/usr/local/aws-cli/v2/*/dist/aws") {
			t.Fatal("documented executable grant did not compose", err)
		}
		commands++
	}
	if commands != 2 {
		t.Fatal("expected both documented agent examples")
	}
}

func TestAWSStatusReportsOnlyStoredPresenceAlongsideNativeStatus(t *testing.T) {
	for _, agent := range []string{"claude", "codex", "gemini"} {
		for _, tc := range []struct {
			name, native string
			keys         []string
			unavailable  bool
		}{
			{name: "absent", native: "missing"},
			{name: "pair", native: "missing", keys: []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"}},
			{name: "temporary", native: "missing", keys: []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN"}},
			{name: "partial", native: "missing", keys: []string{"AWS_ACCESS_KEY_ID"}},
			{name: "token-only", native: "missing", keys: []string{"AWS_SESSION_TOKEN"}},
			{name: "bearer", native: "missing", keys: []string{"AWS_BEARER_TOKEN_BEDROCK"}},
			{name: "native-coexistence", native: "credentials-present", keys: []string{"AWS_BEARER_TOKEN_BEDROCK"}},
			{name: "native-unavailable", keys: []string{"AWS_BEARER_TOKEN_BEDROCK"}, unavailable: true},
		} {
			t.Run(agent+"/"+tc.name, func(t *testing.T) {
				t.Setenv("XDG_CONFIG_HOME", t.TempDir())
				fakeNativeAccounts(t)
				mustCreate(t, "personal")
				acct, err := account.Load("default", "personal")
				if err != nil {
					t.Fatal(err)
				}
				for _, key := range append(tc.keys, "AWS_UNKNOWN") {
					acct.SetSecret(key, "synthetic-private-value")
				}
				if err := acct.Save(); err != nil {
					t.Fatal(err)
				}
				accountStateOperation = func(_ context.Context, _ string, req accountstate.Request) (accountstate.Result, error) {
					if tc.unavailable {
						return accountstate.Result{}, errors.New("native state inaccessible")
					}
					return accountstate.Result{Present: tc.native == "credentials-present", Auth: []accountstate.AuthFinding{{Source: req.Path, Outcome: tc.native}}}, nil
				}
				out, stderr, err := execAccount(t, newAccountAuthStatusCmd(), "", "personal", agent)
				if (err != nil) != tc.unavailable {
					t.Fatal("native availability was hidden", err)
				}
				wantAWS := len(tc.keys) != 0 && agent != "gemini"
				if strings.Contains(out, "AWS secrets present (not verified)") != wantAWS {
					t.Fatal("stored AWS presence was misreported")
				}
				if strings.Contains(out+stderr, "synthetic-private") || strings.Contains(out+stderr, "authenticated") {
					t.Fatal("secret value or provider-authentication claim in status")
				}
				if tc.unavailable && !strings.Contains(out, "unavailable") {
					t.Fatal("unavailable native storage became missing")
				}
				if tc.native == "credentials-present" && !strings.Contains(out, "present") {
					t.Fatal("native login classification disappeared")
				}
			})
		}
	}
}
