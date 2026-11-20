package agentnpm

import (
	"strings"
	"testing"
)

func TestValidatePackage(t *testing.T) {
	// Every name npm can publish or has published stays valid, legacy
	// uppercase and special characters included (archyAstra, archyFable r2).
	for _, ok := range []string{"opencode-ai", "@anthropic-ai/claude-code", "@openai/codex", "@earendil-works/pi-coding-agent",
		"my.cli_2~x", "JSONStream", "@scope/_tool", "@scope/-tool", "x!", "x(y)", "o'brien", "~x", "a*"} {
		if err := ValidatePackage(ok); err != nil {
			t.Errorf("ValidatePackage(%q) = %v", ok, err)
		}
	}
	// Outside npm's URL-safe name set nothing can be a package.
	for _, bad := range []string{"", "a b", "x;rm -rf /", "$(id)", "`id`", "a\nRUN id", "a\"b", "a\\b", "a|b", "a&b",
		"@scope", "@scope/", "@/x", "a/b", "@s/a/b", "x@1.0", strings.Repeat("a", 215)} {
		if err := ValidatePackage(bad); err == nil {
			t.Errorf("ValidatePackage(%q) accepted", bad)
		}
	}
}

func TestShellQuote(t *testing.T) {
	if got := ShellQuote("o'brien@latest"); got != `'o'\''brien@latest'` {
		t.Fatalf("ShellQuote = %s", got)
	}
}
