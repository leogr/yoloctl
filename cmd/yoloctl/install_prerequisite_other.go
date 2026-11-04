//go:build !linux || !amd64

package main

import "context"

func installPrerequisite(ctx context.Context) string {
	if version, ok := limactlVersion(ctx); ok {
		return "Prerequisite: limactl " + version
	}
	return "Prerequisite unavailable: install Lima and put limactl on PATH."
}
