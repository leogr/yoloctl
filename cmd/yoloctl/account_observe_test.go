package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/accountstate"
	"github.com/leogr/yoloctl/internal/agentprofile"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/features"
)

// The command sends one statusline request for the profile's settings and
// reporter, after the consumer check; the guest decides the outcome.
func TestAccountObserveSetsTheProfilesStatusLine(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	fakeNativeAccounts(t)
	mustCreate(t, "personal")
	var sent []accountstate.Request
	outcome := "enabled"
	accountStateOperation = func(_ context.Context, _ string, r accountstate.Request) (accountstate.Result, error) {
		sent = append(sent, r)
		return accountstate.Result{Outcome: outcome}, nil
	}
	out, _, err := execAccount(t, newAccountObserveCmd(), "", "personal", "agy", "--enable")
	if err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 || sent[0].Action != "statusline" || sent[0].Account != "personal" || sent[0].Agent != "agy" || sent[0].Path != "antigravity-cli/settings.json" {
		t.Fatalf("requests = %+v", sent)
	}
	var spec accountstate.StatusLine
	if err := json.Unmarshal(sent[0].Data, &spec); err != nil || spec != (accountstate.StatusLine{Enable: true, Command: features.AgentObserveAgyStatusLine}) {
		t.Fatalf("status line spec = %+v (%v)", spec, err)
	}
	if !strings.Contains(out, "enabled the agy status line reporter in account personal (/home/sandbox/.gemini/antigravity-cli/settings.json)") {
		t.Fatalf("out = %q", out)
	}
	for _, c := range []struct{ flag, outcome, want string }{
		{"--enable", "unchanged", "already enabled"},
		{"--disable", "restored", "previous status line is restored"},
		{"--disable", "removed", "no previous status line was saved, so it is removed"},
		{"--disable", "unchanged", "not enabled"},
		{"--disable", "", "not enabled"},
	} {
		outcome = c.outcome
		out, _, err := execAccount(t, newAccountObserveCmd(), "", "personal", "agy", c.flag)
		if err != nil || !strings.Contains(out, c.want) {
			t.Errorf("%s %q: %q %v, want %q", c.flag, c.outcome, out, err, c.want)
		}
	}
	var spec2 accountstate.StatusLine
	_ = json.Unmarshal(sent[len(sent)-1].Data, &spec2)
	if spec2.Enable {
		t.Fatal("--disable sent enable")
	}
	// No settings yet: refused, and the launcher's first start named.
	for _, missing := range []string{"absent", ""} {
		outcome = missing
		_, _, err := execAccount(t, newAccountObserveCmd(), "", "personal", "agy", "--enable")
		if err == nil || !strings.Contains(err.Error(), "has no agy settings yet") || !strings.Contains(err.Error(), "seeded with the reporter by the agent's launcher at its first start") {
			t.Errorf("enable with %q settings: %v", missing, err)
		}
	}
}

// Like native import: a running consumer, a missing account, an agent with
// no account status line or a wrong flag set writes nothing.
func TestAccountObserveRefusesWithoutWriting(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	fakeNativeAccounts(t)
	mustCreate(t, "personal")
	written := 0
	accountStateOperation = func(context.Context, string, accountstate.Request) (accountstate.Result, error) {
		written++
		return accountstate.Result{}, nil
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"personal", "agy"}, "exactly one of --enable or --disable"},
		{[]string{"personal", "agy", "--enable", "--disable"}, "none of the others can be"},
		{[]string{"personal", "claude", "--enable"}, "reports without the account's settings"},
		{[]string{"missing", "agy", "--enable"}, "missing"},
		{[]string{"../x", "agy", "--enable"}, "account"},
	} {
		_, _, err := execAccount(t, newAccountObserveCmd(), "", c.args...)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v, want %q", c.args, err, c.want)
		}
	}
	accountStopUsers = func(context.Context, string, string, bool, io.Writer) error {
		return errors.New("account personal is used by running box coder; stop its boxes before modifying native state")
	}
	if _, _, err := execAccount(t, newAccountObserveCmd(), "", "personal", "agy", "--enable"); err == nil || !strings.Contains(err.Error(), "running box coder") {
		t.Fatalf("active consumer: %v", err)
	}
	if written != 0 {
		t.Fatalf("%d native writes after refusals", written)
	}
}

// The controller owns the mutation, as for native import; the profile names
// the launcher the feature installs.
func TestAccountObserveIsAControllerMutation(t *testing.T) {
	if op := controllerOperations["account.observe"]; op != (controller.Operation{Mutates: true, EffectGated: true, Footprint: controller.FootprintAccount}) || controllerOperations["account.auth.import"] != op {
		t.Fatalf("account.observe = %+v, want account.auth.import's class", op)
	}
	agy, err := agentprofile.Lookup("agy")
	if err != nil {
		t.Fatal(err)
	}
	if line := agy.Session.Observe.AccountStatusLine; line == nil || line.Command != features.AgentObserveAgyStatusLine {
		t.Fatalf("agy account status line = %+v, want the feature's launcher", line)
	}
}
