package controller

import (
	"errors"
	"os"
	"sync/atomic"
	"testing"
)

// After expiry a source drains only the bytes ready at that moment: bytes the
// writer appends after the first expired read are left, and the source, its
// writer still open, is held.
func TestDrainSourceReadsOnlyTheBytesReadyAtExpiry(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	src, err := pollableDup(r)
	_ = r.Close()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	if _, err := w.WriteString("queued"); err != nil {
		t.Fatal(err)
	}
	var expired, held atomic.Bool
	expired.Store(true)
	left := -1
	drain := drainSource{src, &expired, &held, &left}
	buf := make([]byte, 64)
	n, err := drain.Read(buf)
	if err != nil || string(buf[:n]) != "queued" {
		t.Fatalf("first expired read = %q, %v", buf[:n], err)
	}
	if _, err := w.WriteString("appended"); err != nil {
		t.Fatal(err)
	}
	n, err = drain.Read(buf)
	if n != 0 || !errors.Is(err, errOutputHeld) || !held.Load() {
		t.Fatalf("read past the bytes ready at expiry: %q, %v, held=%v", buf[:n], err, held.Load())
	}
}
