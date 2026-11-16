package vmapi

import "time"

// BuilderReadyTimeout is the scoped total budget of each BuildKit startup or
// connection readiness phase. It is not a build, diagnostic or cleanup timeout.
const BuilderReadyTimeout = 2 * time.Minute

// ContainerdReadyTimeout bounds the boot-time containerd readiness phase.
const ContainerdReadyTimeout = 2 * time.Minute
