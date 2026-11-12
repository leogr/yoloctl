//go:build race

package mermaid

import (
	"testing"
	"time"
)

// skipUnderRace skips a test that asserts the real 2 s/4 s limits: the race
// detector slows the WASM compiler and guest enough to change the outcome.
// The ordinary suite still runs it.
func skipUnderRace(t *testing.T) { t.Skip("real-time renderer limits are not meaningful under -race") }

// testGuard is the client guard in tests: the real one, except under -race,
// where starting the instrumented worker alone can exceed it.
const testGuard = time.Minute
