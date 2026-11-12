package mermaid

import (
	"container/list"
	"crypto/sha256"
	"encoding/binary"
	"sync"
)

const (
	cacheEntries = 64
	cacheBytes   = 4 << 20
)

// cacheKey identifies one rendering: the source bytes, the requested width,
// the width method and the renderer artifact.
type cacheKey [sha256.Size]byte

func newCacheKey(source string, width int, method string) cacheKey {
	h := sha256.New()
	var n [8]byte
	for _, part := range []string{moduleHash(), method, source} {
		binary.BigEndian.PutUint64(n[:], uint64(len(part)))
		_, _ = h.Write(n[:])
		_, _ = h.Write([]byte(part))
	}
	binary.BigEndian.PutUint64(n[:], uint64(width))
	_, _ = h.Write(n[:])
	var k cacheKey
	copy(k[:], h.Sum(nil))
	return k
}

// resultCache is a per-process LRU of rendering outcomes, bounded to 64
// entries and 4 MiB of drawing text. Nothing is written to disk.
type resultCache struct {
	mu      sync.Mutex
	order   *list.List // front = most recent
	entries map[cacheKey]*list.Element
	bytes   int
}

type cacheEntry struct {
	key    cacheKey
	result Result
	size   int
}

func newResultCache() *resultCache {
	return &resultCache{order: list.New(), entries: map[cacheKey]*list.Element{}}
}

func resultSize(r Result) int {
	n := 0
	for _, line := range r.Lines {
		n += len(line) + 1
	}
	return n
}

func (c *resultCache) get(k cacheKey) (Result, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[k]
	if !ok {
		return Result{}, false
	}
	c.order.MoveToFront(e)
	r := e.Value.(*cacheEntry).result
	r.Lines = append([]string(nil), r.Lines...)
	return r, true
}

func (c *resultCache) put(k cacheKey, r Result) {
	size := resultSize(r)
	if size > cacheBytes {
		return
	}
	r.Lines = append([]string(nil), r.Lines...)
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[k]; ok {
		c.bytes -= e.Value.(*cacheEntry).size
		c.order.Remove(e)
		delete(c.entries, k)
	}
	c.entries[k] = c.order.PushFront(&cacheEntry{key: k, result: r, size: size})
	c.bytes += size
	for c.order.Len() > cacheEntries || c.bytes > cacheBytes {
		last := c.order.Back()
		entry := last.Value.(*cacheEntry)
		c.order.Remove(last)
		delete(c.entries, entry.key)
		c.bytes -= entry.size
	}
}
