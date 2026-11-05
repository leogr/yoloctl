package agentprofile

import "testing"

func TestAdditionalStateMountValidation(t *testing.T) {
	for _, tc := range []struct {
		source, dest string
		valid        bool
	}{
		{"anthropic", "/home/sandbox/.config/anthropic", true},
		{"../other", "/home/sandbox/.config/anthropic", false},
		{".", "/home/sandbox/.config/anthropic", false},
		{"/var/secret", "/home/sandbox/.config/anthropic", false},
		{"anthropic", "/", false},
		{"anthropic", "/home/sandbox", false},
		{"anthropic", "/home/sandbox/.claude/nested", false},
		{"anthropic", "/home/sandbox/.claude", false},
		{"anthropic", "/home/sandbox/.config/../anthropic", false},
	} {
		t.Run(tc.source+tc.dest, func(t *testing.T) {
			p := &Profile{Name: "fixture", Image: "fixture:latest", Command: "fixture", StateContainerPath: "/home/sandbox/.claude", StateMounts: []StateMount{{Source: tc.source, Destination: tc.dest}}}
			if err := p.Validate(); (err == nil) != tc.valid {
				t.Fatalf("valid=%t error=%v", tc.valid, err)
			}
		})
	}
}

func TestClaudeAdditionalStateAndAuthFormats(t *testing.T) {
	p, err := Lookup("claude")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.StateMounts) != 1 || p.StateMounts[0].Source != "anthropic" || p.StateMounts[0].Destination != "/home/sandbox/.config/anthropic" || p.Auth.Format != "claude" || p.Auth.ProfilesDir != "anthropic" {
		t.Fatalf("unexpected Claude state mapping: %+v", p.StateMounts)
	}
	codex, err := Lookup("codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(codex.StateMounts) != 0 || codex.Auth.Format != "codex" {
		t.Fatal("Codex state mapping unexpectedly changed")
	}
	p.StateMounts = nil
	if err := p.Validate(); err == nil {
		t.Fatal("accepted profile auth without a persistent view")
	}
}
