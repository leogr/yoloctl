package session

import (
	"bytes"
	"strings"
	"testing"
)

func TestRingKeepsMostRecentBytes(t *testing.T) {
	r := NewRing(10)
	r.Write([]byte("abcdef"))
	if got := string(r.Snapshot()); got != "abcdef" {
		t.Fatalf("snapshot = %q, want abcdef", got)
	}
	r.Write([]byte("ghijkl")) // 12 total, keeps last 10
	if got := string(r.Snapshot()); got != "cdefghijkl" {
		t.Fatalf("snapshot = %q, want cdefghijkl", got)
	}
	if got := len(r.Snapshot()); got != 10 {
		t.Fatalf("len = %d, want 10", got)
	}
}

func TestRingOversizeWriteKeepsTail(t *testing.T) {
	r := NewRing(4)
	r.Write([]byte("0123456789"))
	if got := string(r.Snapshot()); got != "6789" {
		t.Fatalf("snapshot = %q, want 6789", got)
	}
}

func TestRingManySmallWrites(t *testing.T) {
	r := NewRing(64)
	var want bytes.Buffer
	for i := range 1000 {
		chunk := strings.Repeat(string(rune('a'+i%26)), 7)
		r.Write([]byte(chunk))
		want.WriteString(chunk)
	}
	tail := want.Bytes()[want.Len()-64:]
	if got := r.Snapshot(); !bytes.Equal(got, tail) {
		t.Fatalf("snapshot = %q, want %q", got, tail)
	}
}

func TestRingSnapshotIsACopy(t *testing.T) {
	r := NewRing(8)
	r.Write([]byte("hello"))
	snap := r.Snapshot()
	r.Write([]byte("WORLD"))
	if string(snap) != "hello" {
		t.Fatalf("snapshot mutated to %q", snap)
	}
}

func TestRingEvictionPreservesStream(t *testing.T) {
	for _, capacity := range []int{1, 4, 64} {
		r := NewRing(capacity)
		var original, evicted []byte
		for _, chunk := range []string{"hello", "", "\x1b[>3u", strings.Repeat("界", 40), "tail"} {
			p := []byte(chunk)
			original = append(original, p...)
			evicted = append(evicted, r.Write(p)...)
			got := append(append([]byte(nil), evicted...), r.Snapshot()...)
			if !bytes.Equal(got, original) {
				t.Fatalf("capacity %d: eviction reordered or lost bytes", capacity)
			}
		}
	}
}
