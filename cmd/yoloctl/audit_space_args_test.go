package main

import (
	"io"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

func TestAuditReadCommandsAcceptAnOptionalSpace(t *testing.T) {
	t.Setenv("YOLOCTL_SPACE", "contextual")
	for _, action := range []string{"logs", "export"} {
		for _, tc := range []struct {
			name string
			args []string
			want string
			bad  bool
		}{
			{"positional", []string{"yolo"}, "yolo", false},
			{"flag", []string{"--space", "flagged"}, "flagged", false},
			{"same explicit", []string{"yolo", "--space", "yolo"}, "yolo", false},
			{"conflicting explicit", []string{"yolo", "--space", "other"}, "", true},
			{"context fallback", nil, "contextual", false},
			{"invalid space", []string{"../escape"}, "", true},
			{"extra argument", []string{"one", "two"}, "", true},
			{"invalid box", []string{"yolo", "--box", "../escape"}, "", true},
		} {
			t.Run(action+"/"+tc.name, func(t *testing.T) {
				root := newAuditCmd()
				leaf, _, err := root.Find([]string{action})
				if err != nil {
					t.Fatal(err)
				}
				reached := false
				got := ""
				leaf.RunE = func(cmd *cobra.Command, _ []string) error {
					reached = true
					var err error
					got, err = selectedSpace(cmd)
					return err
				}
				args := append([]string{action}, tc.args...)
				if action == "export" {
					args = append(args, "--output", filepath.Join(t.TempDir(), "events.jsonl"))
				}
				root.SetArgs(args)
				root.SetOut(io.Discard)
				root.SetErr(io.Discard)
				err = root.ExecuteContext(t.Context())
				if tc.bad {
					if err == nil || reached {
						t.Fatalf("invalid target/filter reached execution: %v reached=%v", err, reached)
					}
					return
				}
				if err != nil || !reached || got != tc.want {
					t.Fatalf("selected %q, want %q: %v", got, tc.want, err)
				}
			})
		}
	}
}
