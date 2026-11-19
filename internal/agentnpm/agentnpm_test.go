package agentnpm

import (
	"strings"
	"testing"
)

func TestValidatePackage(t *testing.T) {
	for _, ok := range []string{"opencode-ai", "@anthropic-ai/claude-code", "@openai/codex", "@earendil-works/pi-coding-agent", "my.cli_2~x"} {
		if err := ValidatePackage(ok); err != nil {
			t.Errorf("ValidatePackage(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Upper", "a b", "x;rm -rf /", "$(id)", "`id`", "a\nRUN id", "@scope", "@scope/", "@/x", "a/b", "@s/a/b", ".x", "_x", "-x", "x@1.0", strings.Repeat("a", 215)} {
		if err := ValidatePackage(bad); err == nil {
			t.Errorf("ValidatePackage(%q) accepted", bad)
		}
	}
}
