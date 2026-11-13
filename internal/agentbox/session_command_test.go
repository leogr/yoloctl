package agentbox

import (
	"fmt"
	"os"
	"reflect"
	"testing"
)

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

// A declaration whose strings yaml.v3 would write as block scalars it cannot
// read (a multi-line string starting with a newline or an indented line, as in
// fable-architect2's reproduction) is saved so that it loads back unchanged:
// a session command, an env literal and an agent argument.
func TestSaveLoadsBackMultilineStrings(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	workload, err := os.ReadFile("../strictyaml/testdata/stop-box-workload.txt")
	if err != nil {
		t.Fatal(err)
	}
	for i, s := range []string{string(workload), "\na\nb", "\na\n b: c\n", " a\nb: c"} {
		agentless := &Config{Name: fmt.Sprintf("room%d", i), Space: "default", Agent: "none", Account: "none", Image: "yoloctl/agentbox:ubuntu-24.04",
			SessionCommand: []string{"python3", "-u", "-c", s, `{"nonce": "abc"}`}, Workdir: NewEphemeralWorkdir(fmt.Sprintf("room%d", i)),
			Env: map[string]EnvVar{"SCRIPT": {Literal: s}}}
		agent := &Config{Name: fmt.Sprintf("coder%d", i), Space: "default", Agent: "claude", Account: "default", Image: "yoloctl/agentbox:ubuntu-24.04",
			AgentExtraArgs: []string{"--append-system-prompt", s}, Workdir: NewEphemeralWorkdir(fmt.Sprintf("coder%d", i)),
			Env: map[string]EnvVar{"NOTE": {Literal: s}}}
		for _, want := range []*Config{agentless, agent} {
			if err := want.Save(); err != nil {
				t.Fatalf("%d %s: Save: %v", i, want.Name, err)
			}
			got, err := Load("default", want.Name)
			if err != nil {
				t.Fatalf("%d %s: Load: %v", i, want.Name, err)
			}
			got.path, want.path = "", ""
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("%d %s: loaded %+v, saved %+v", i, want.Name, got, want)
			}
		}
	}
}
