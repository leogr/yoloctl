package agentprofile

import "testing"

func TestBuiltinCredentialStorageFormats(t *testing.T) {
	claude, err := Lookup("claude")
	if err != nil {
		t.Fatal(err)
	}
	if claude.Auth.Format != AuthFormatClaude || claude.Auth.ProfilesDir != "anthropic" || claude.LaunchEnv["ANTHROPIC_CONFIG_DIR"] != "{{state_dir}}/anthropic" {
		t.Fatal("Claude profile store is not relocated into its native leaf")
	}
	codex, err := Lookup("codex")
	if err != nil {
		t.Fatal(err)
	}
	if codex.Auth.Format != AuthFormatCodex || codex.StateContainerPath != "/home/sandbox/.codex" || len(codex.LaunchEnv) != 0 {
		t.Fatal("Codex acquired a mount/store override")
	}
	for _, tc := range []struct{ format, dir string }{
		{"unknown", ""}, {AuthFormatCodex, "anthropic"},
		{AuthFormatClaude, "../outside"}, {AuthFormatClaude, "/absolute"},
	} {
		claude.Auth.Format, claude.Auth.ProfilesDir = tc.format, tc.dir
		if err := claude.Validate(); err == nil {
			t.Fatal("accepted unsupported native credential format/path", tc)
		}
	}
}
