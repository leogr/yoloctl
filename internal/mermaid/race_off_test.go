//go:build !race

package mermaid

import "testing"

func skipUnderRace(*testing.T) {}

// testGuard is the client guard in tests: the real one, except under -race,
// where starting the instrumented worker alone can exceed it.
const testGuard = renderGuard
