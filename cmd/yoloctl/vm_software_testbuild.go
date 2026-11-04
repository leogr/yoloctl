//go:build microvmtest

package main

import (
	"time"

	"github.com/leogr/yoloctl/internal/operator"
)

// Software VM integration tests may run inside an operator-enabled coding box.
// This separately built test executable exercises its private simulated host;
// it must never inherit authority over the coding session's real space. The
// production binary retains fixed-mount operator detection without overrides.
func init() {
	loadOperatorClient = func() (*operator.Client, error) { return nil, nil }
	// TCG executes the guest's Go/WASM compilation in software. This gate
	// checks functionality; the production 10s bound and KVM timing are separate.
	holderStartupTimeout = 90 * time.Second
}
