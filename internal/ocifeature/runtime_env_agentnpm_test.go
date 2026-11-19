package ocifeature

import (
	"strings"
	"testing"
)

// The managed npm prefix stays out of the writable-folder alias checks
// (archyFable F1): /opt/yoloctl is already refused by linkGuard, and listing
// it would rename the image of every box with a writable Feature folder.
func TestAliasGuardLeavesTheManagedNpmPrefixToLinkGuard(t *testing.T) {
	for _, prefix := range networkToolPrefixes() {
		if strings.HasPrefix(prefix, "/opt/yoloctl") {
			t.Fatalf("network tool prefixes include %s", prefix)
		}
	}
	guard := linkGuard([]string{"/go"}, []string{"/go"})
	if strings.Contains(guard, "/opt/yoloctl/npm") || !strings.Contains(guard, "/opt/yoloctl/*") {
		t.Fatalf("guard text:\n%s", guard)
	}
}
