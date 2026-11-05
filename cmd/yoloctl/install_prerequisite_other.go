//go:build !linux || !amd64

package main

import "context"

// installPrerequisite is the failed host prerequisite, or "" when it holds.
func installPrerequisite(ctx context.Context) string {
	if _, ok := limactlVersion(ctx); ok {
		return ""
	}
	return "Lima is needed: install it and put limactl on PATH"
}
