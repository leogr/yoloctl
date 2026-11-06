package agentprofile

import "testing"

func TestResumeFilePatternValidation(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		valid   bool
	}{
		{"projects/*/{{session_id}}.jsonl", true},
		{"data/projects/*/session-{{session_id}}.jsonl", true},
		{"/projects/*/{{session_id}}.jsonl", false},
		{"../projects/*/{{session_id}}.jsonl", false},
		{"projects/**/{{session_id}}.jsonl", false},
		{"projects/*/*/{{session_id}}.jsonl", false},
		{"projects/*/nested/{{session_id}}.jsonl", false},
		{"projects/*/other.jsonl", false},
		{"projects/*/{{session_id}}", false},
		{"projects/*/{{session_id}}-{{session_id}}.jsonl", false},
		{"projects/*/{{session_id}}.jsonl;cmd", false},
		{"projects/*/{{other}}.jsonl", false},
	} {
		t.Run(tc.pattern, func(t *testing.T) {
			p := &Profile{Name: "fixture", Image: "base", Command: "tool", StateContainerPath: "/home/sandbox/.tool", Session: &SessionSpec{
				IDFormat: SessionIDFormatUUID, AssignArgs: []string{"--session-id", "{{session_id}}"},
				ResumeArgv: []string{"{{command}}", "--resume", "{{session_id}}"}, ResumeFile: tc.pattern,
			}}
			if err := p.Validate(); (err == nil) != tc.valid {
				t.Fatalf("valid=%t error=%v", tc.valid, err)
			}
			if tc.valid {
				p.StateContainerPath = ""
				if err := p.Validate(); err == nil {
					t.Fatal("resume file without native state")
				}
				p.StateContainerPath = "/home/sandbox/.tool"
				p.Session.AssignArgs = nil
				if err := p.Validate(); err == nil {
					t.Fatal("resume file without fresh assignment support")
				}
			}
		})
	}
}
