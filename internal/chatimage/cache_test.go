package chatimage

import (
	"strconv"
	"testing"
)

func TestCacheBoundsEntriesAndBytes(t *testing.T) {
	var c Cache
	for i := 0; i < CacheEntries; i++ {
		c.Put(strconv.Itoa(i), Prepared{PNG: []byte{1}})
	}
	c.Get("0") // most recently used now
	c.Put("new", Prepared{PNG: []byte{1}})
	if _, ok := c.items["1"]; ok || len(c.items) != CacheEntries {
		t.Fatalf("entries %d, least recently used kept", len(c.items))
	}
	if _, ok := c.items["0"]; !ok {
		t.Fatal("a recently used entry was evicted")
	}
	big := Prepared{PNG: make([]byte, 9<<20)}
	c.Put("big1", big)
	c.Put("big2", big)
	if _, ok := c.items["big1"]; ok || c.bytes > CacheBytes {
		t.Fatalf("bytes %d over the bound", c.bytes)
	}
	c.Put("huge", Prepared{PNG: make([]byte, CacheBytes+1)})
	if _, ok := c.items["huge"]; ok {
		t.Fatal("an entry larger than the cache was stored")
	}
	total := 0
	for _, p := range c.items {
		total += len(p.PNG)
	}
	if total != c.bytes || len(c.keys) != len(c.items) {
		t.Fatalf("accounting: bytes %d/%d keys %d items %d", c.bytes, total, len(c.keys), len(c.items))
	}
}

func TestCacheKeySeparatesIdentitiesAndCellSize(t *testing.T) {
	f := Frame{Artifact: "a", SHA256: "h", RoomID: "r", DiskID: "d", HumanID: "u", ChannelID: "c"}
	base := CacheKey(f, 10, 20)
	g := f
	g.HumanID = "other"
	if CacheKey(g, 10, 20) == base || CacheKey(f, 11, 20) == base || CacheKey(f, 10, 20) != base {
		t.Fatal("cache key ignores an identity or the cell size")
	}
	var c Cache
	c.Put(base, Prepared{PNG: []byte{1}})
	c.Clear()
	if _, ok := c.Get(base); ok || c.bytes != 0 {
		t.Fatal("Clear kept an entry")
	}
}
