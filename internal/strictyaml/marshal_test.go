package strictyaml

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type declaration struct {
	SessionCommand []string          `yaml:"session_command,omitempty"`
	Env            map[string]string `yaml:"env,omitempty"`
	AgentExtraArgs []string          `yaml:"agent_extra_args,omitempty"`
}

// The strings yaml.Marshal (v3.0.1) writes as block scalars it cannot read
// back (fable-architect2's reproduction, step0/yaml-blocker-1.md) are written
// so that they read back exactly, in a command, an env literal and an agent
// argument; strings it handles keep yaml.Marshal's bytes.
func TestMarshalReadsBackWhatYAMLV3CannotParse(t *testing.T) {
	workload, err := os.ReadFile("testdata/stop-box-workload.txt")
	if err != nil {
		t.Fatal(err)
	}
	broken := map[string]string{
		"stop-box workload":         string(workload),
		"leading newline":           "\na\nb",
		"leading newline, indented": "\na\n b: c\n",
		"indented first line":       " a\nb: c",
	}
	fine := map[string]string{
		"two plain lines":          "a\nb",
		"trailing space on a line": "a \nb: c\n",
		"one line":                 "plain",
	}
	for name, s := range mergeCases(broken, fine) {
		for field, d := range map[string]declaration{
			"session_command":  {SessionCommand: []string{"python3", "-u", "-c", s, `{"nonce": "abc"}`}},
			"env":              {Env: map[string]string{"SCRIPT": s, "OTHER": "x"}},
			"agent_extra_args": {AgentExtraArgs: []string{"--prompt", s}},
		} {
			// The reproduction: yaml.Marshal's bytes do not read back as d.
			plain, err := yaml.Marshal(d)
			if err != nil {
				t.Fatal(err)
			}
			var probe declaration
			_, isBroken := broken[name]
			// (As a mapping value an indented first line happens to survive.)
			breaks := isBroken && (name != "indented first line" || field != "env")
			if plainOK := yaml.Unmarshal(plain, &probe) == nil && equalDeclarations(probe, d); plainOK == breaks {
				t.Fatalf("%s/%s: yaml.Marshal reads back %v, reproduced %v", name, field, plainOK, !breaks)
			}
			raw, err := Marshal(d)
			if err != nil {
				t.Fatalf("%s/%s: %v", name, field, err)
			}
			var back declaration
			if err := Decode(raw, &back); err != nil {
				t.Fatalf("%s/%s: does not read back: %v\n%s", name, field, err, raw)
			}
			if !equalDeclarations(back, d) {
				t.Fatalf("%s/%s: read back %+v", name, field, back)
			}
			if !breaks && string(raw) != string(plain) {
				t.Fatalf("%s/%s: changed bytes yaml.Marshal reads back:\n%s\nvs\n%s", name, field, raw, plain)
			}
		}
	}
}

func mergeCases(a, b map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func equalDeclarations(a, b declaration) bool {
	if strings.Join(a.SessionCommand, "\x00") != strings.Join(b.SessionCommand, "\x00") ||
		strings.Join(a.AgentExtraArgs, "\x00") != strings.Join(b.AgentExtraArgs, "\x00") || len(a.Env) != len(b.Env) {
		return false
	}
	for k, v := range a.Env {
		if b.Env[k] != v {
			return false
		}
	}
	return true
}
