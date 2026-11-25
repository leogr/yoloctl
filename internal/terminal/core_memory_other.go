//go:build !unix

package terminal

import "context"

// withCoreMemory keeps wazero's own memory growth where the Unix reservation
// in core_memory_unix.go is not available.
var withCoreMemory = func(ctx context.Context) context.Context { return ctx }
