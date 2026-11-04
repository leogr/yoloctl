package main

import (
	"slices"
	"testing"
)

func TestNpmInstallGlobalAtLatestArgsUsesSandboxOwnedPrefixAndCache(t *testing.T) {
	got := npmInstallGlobalAtLatestArgs("coder", "@vendor/agent")
	// The paths are literal on purpose, not features.NPMGlobalPrefix: the
	// base-image chown, composed-build chown -R, smoke harness, and capability
	// policies all pin /home/sandbox/.npm-global absolutely, so a drift of the
	// const must fail here instead of silently moving the ownership contract.
	want := []string{
		"sudo", "nerdctl", "exec",
		"--user", "sandbox",
		"coder",
		"npm", "install", "-g",
		"--prefix=/home/sandbox/.npm-global",
		"--cache=/home/sandbox/.npm-global/.cache/npm",
		"--prefer-online",
		"@vendor/agent@latest",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("npm install args = %q, want %q", got, want)
	}
}
