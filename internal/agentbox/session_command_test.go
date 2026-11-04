package agentbox

import "testing"

func TestSessionCommandValidation(t *testing.T) {
	for _, tc := range []struct {
		name, agent, account string
		argv                 []string
		valid                bool
	}{
		{"shell", "none", "none", nil, true},
		{"service", "none", "none", []string{"aircd", "serve", "--config", ".airc/server.toml"}, true},
		{"empty argument", "none", "none", []string{"printf", ""}, true},
		{"empty executable", "none", "none", []string{" "}, false},
		{"nul", "none", "none", []string{"echo", "a\x00b"}, false},
		{"real agent", "claude", "default", []string{"aircd"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{Name: "room", Space: "default", Agent: tc.agent, Account: tc.account, SessionCommand: tc.argv, Workdir: NewEphemeralWorkdir("room")}
			if err := cfg.Validate(); (err == nil) != tc.valid {
				t.Fatalf("Validate = %v, want valid %v", err, tc.valid)
			}
		})
	}
}
