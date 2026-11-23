// Package catalogsnapshot shares parsed catalogs within a controller worker's
// private snapshot.
package catalogsnapshot

import (
	"sync"
	"sync/atomic"

	"github.com/leogr/yoloctl/internal/spaceenv"
)

var generation, selected atomic.Uint64

// Use selects a fresh copy so each loader will read it on first use.
// The worker calls it before dispatch and keeps the selection until it exits.
func Use(path string) {
	spaceenv.UseCatalogSnapshot(path)
	if path == "" {
		selected.Store(0)
		return
	}
	selected.Store(generation.Add(1))
}

// Loader reuses the load result, including errors, for the selected snapshot.
// Without a snapshot it reads the live catalog on each call. Callers treat
// shared results as read-only.
func Loader[T any](load func() (T, error)) func() (T, error) {
	var mu sync.Mutex
	var loaded uint64
	var value T
	var err error
	return func() (T, error) {
		snapshot := selected.Load()
		if snapshot == 0 {
			return load()
		}
		mu.Lock()
		defer mu.Unlock()
		if loaded != snapshot {
			value, err = load()
			loaded = snapshot
		}
		return value, err
	}
}
