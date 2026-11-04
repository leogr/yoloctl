package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestFlagHelpDoesNotPromoteExamplesToMetavariables(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		command   func() *cobra.Command
		forbidden string
		want      string
	}{
		{
			name:      "box create features",
			command:   newAgentboxCreateCmd,
			forbidden: "--features yoloctl image build --features FEATURE --tag REF",
			want:      "manual yoloctl image build --features FEATURE --tag REF path",
		},
		{
			name:      "space create no-start",
			command:   newSpaceCreateCmd,
			forbidden: "--no-start yoloctl space start",
			want:      "Initialize the account disk, then leave the VM stopped",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := tc.command()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			if err := cmd.Help(); err != nil {
				t.Fatalf("render help: %v", err)
			}
			help := out.String()
			if strings.Contains(help, tc.forbidden) {
				t.Fatalf("help promoted an example to a flag metavariable:\n%s", help)
			}
			if !strings.Contains(help, tc.want) {
				t.Fatalf("help lost the intended usage guidance %q:\n%s", tc.want, help)
			}
		})
	}
}

func TestBoxCreateDoesNotExposeRemovedWorkdirSync(t *testing.T) {
	t.Parallel()

	cmd := newAgentboxCreateCmd()
	if flag := cmd.Flags().Lookup("sync"); flag != nil {
		t.Fatalf("box create still exposes removed --sync flag: %+v", flag)
	}
}
