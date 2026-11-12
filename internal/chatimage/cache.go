package chatimage

import (
	"strconv"
	"strings"
)

// Cache bounds (plan §3C): prepared previews kept in memory per host owner.
const (
	CacheEntries = 8
	CacheBytes   = 16 << 20
)

// CacheKey identifies a prepared preview: the artifact, the identities it
// was read under and the cell size it was scaled for.
func CacheKey(f Frame, cellW, cellH int) string {
	return strings.Join([]string{f.Artifact, f.SHA256, f.RoomID, f.DiskID, f.HumanID, f.ChannelID, strconv.Itoa(cellW), strconv.Itoa(cellH)}, "|")
}

// Cache is an in-memory LRU of host-made preview PNGs, bounded by entries
// and bytes. It lives only as long as its owner; the zero value is empty.
// It is not safe for concurrent use.
type Cache struct {
	keys  []string // least recently used first
	items map[string]Prepared
	bytes int
}

// Get returns the preview under key and marks it recently used.
func (c *Cache) Get(key string) (Prepared, bool) {
	p, ok := c.items[key]
	if ok {
		c.touch(key)
	}
	return p, ok
}

// Put stores p under key, evicting the least recently used previews beyond
// the bounds. A preview larger than the whole cache is not stored.
func (c *Cache) Put(key string, p Prepared) {
	if len(p.PNG) > CacheBytes {
		return
	}
	if c.items == nil {
		c.items = map[string]Prepared{}
	}
	if old, ok := c.items[key]; ok {
		c.bytes -= len(old.PNG)
	}
	c.items[key] = p
	c.bytes += len(p.PNG)
	c.touch(key)
	for len(c.keys) > CacheEntries || c.bytes > CacheBytes {
		oldest := c.keys[0]
		c.keys = c.keys[1:]
		c.bytes -= len(c.items[oldest].PNG)
		delete(c.items, oldest)
	}
}

// Clear empties the cache.
func (c *Cache) Clear() { *c = Cache{} }

func (c *Cache) touch(key string) {
	for i, k := range c.keys {
		if k == key {
			c.keys = append(c.keys[:i], c.keys[i+1:]...)
			break
		}
	}
	c.keys = append(c.keys, key)
}
