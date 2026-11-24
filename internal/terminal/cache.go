package terminal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/tetratelabs/wazero"
)

// The compiled core can be kept on disk, so a process loads it instead of
// compiling it again. The cache holds native code that this process runs, and
// yoloctl trusts whoever can write or replace the selected cache folder:
// YOLOCTL_HOME or XDG_CONFIG_HOME can place it outside the login home, including
// on shared or restored storage. Any cache failure ends in the in-memory compile.

const memoryLimitPages = 4096

var cacheDir atomic.Pointer[func() string]

// UseCompilationCache selects the disk cache for the compiled core. dir is read
// once, when the process creates its first terminal, and may return "" for none.
// Without a call the core compiles in memory, as callers other than the CLI and
// the session holder do.
func UseCompilationCache(dir func() string) { cacheDir.Store(&dir) }

func runtimeConfig() wazero.RuntimeConfig {
	return wazero.NewRuntimeConfig().WithMemoryLimitPages(memoryLimitPages).WithCloseOnContextDone(true)
}

// buildKey names the folder for this core and its runtime options: one entry
// per build stays together. wazero's own entry key already covers the module
// (including the termination option) and the CPU features.
func buildKey() string {
	h := sha256.New()
	h.Write(artifact)
	_, _ = fmt.Fprintf(h, "\x00pages=%d,close-on-done=true", memoryLimitPages)
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func compileCore() (*compiledCore, error) {
	if f := cacheDir.Load(); f != nil {
		if dir := (*f)(); dir != "" {
			if c, ok := compileCached(dir); ok {
				return c, nil
			}
		}
	}
	return buildCore(runtimeConfig())
}

// compileCached compiles through the disk cache in dir/<build key>. wazero fails
// the compile when it cannot write an entry, but keeps the compiled module in the
// cache's memory, so one retry with a new runtime reuses it. Any other error
// (the folder cannot be created, a corrupt or unreadable entry) ends in the
// caller's in-memory compile; the entry stays, since a concurrent process may
// have just replaced it.
func compileCached(dir string) (*compiledCore, bool) {
	key := filepath.Join(dir, buildKey())
	if err := os.MkdirAll(key, 0o700); err != nil {
		return nil, false
	}
	cache, err := wazero.NewCompilationCacheWithDir(key)
	if err != nil {
		return nil, false
	}
	for range 2 {
		if c, err := buildCore(runtimeConfig().WithCompilationCache(cache)); err == nil {
			return c, true
		}
	}
	_ = cache.Close(context.Background())
	return nil, false
}
