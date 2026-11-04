//go:build linux && amd64

package firecracker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/vmapi"
	"github.com/leogr/yoloctl/internal/vmwire"
)

func TestVerifiedImageRejectsChangedArtifacts(t *testing.T) {
	dir := t.TempDir()
	digest := sha256.Sum256([]byte("fixture"))
	sum := hex.EncodeToString(digest[:])
	m := Image{Schema: 1, Architecture: "amd64", GuestProtocol: vmwire.Protocol, KernelSHA256: sum, RootfsSHA256: sum, FirecrackerSHA256: sum}
	for _, name := range []string{"kernel", "rootfs.ext4", "firecracker"} {
		if e := os.WriteFile(filepath.Join(dir, name), []byte("fixture"), 0600); e != nil {
			t.Fatal(e)
		}
	}
	raw, _ := json.Marshal(m)
	if e := os.WriteFile(filepath.Join(dir, "image.json"), raw, 0600); e != nil {
		t.Fatal(e)
	}
	got, files, e := VerifyImage(dir)
	if e != nil {
		t.Fatal(e)
	}
	c := Config{Image: got, ImageFiles: files, Spec: vmapi.Spec{Image: dir}}
	if e = unchangedImage(c); e != nil {
		t.Fatal(e)
	}
	// A reboot or remount may renumber the host device of the same,
	// unchanged file (seen after a power loss): still the verified artifact.
	renumbered := Config{Image: c.Image, ImageFiles: map[string]artifactStamp{}, Spec: c.Spec}
	for name, st := range c.ImageFiles {
		st.Device += 30
		renumbered.ImageFiles[name] = st
	}
	if e = unchangedImage(renumbered); e != nil {
		t.Fatal("device-only renumbering refused:", e)
	}
	path := filepath.Join(dir, "kernel")
	st, _ := os.Stat(path)
	// The emulated test host updates inode timestamps at clock-tick granularity.
	time.Sleep(20 * time.Millisecond)
	if e = os.WriteFile(path, []byte("changed"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.Chtimes(path, st.ModTime(), st.ModTime()); e != nil {
		t.Fatal(e)
	}
	if e = unchangedImage(c); e == nil {
		t.Fatal("accepted same-size artifact changed with restored mtime")
	}
	if e = unchangedImage(renumbered); e == nil {
		t.Fatal("device renumbering hid a rewritten artifact")
	}
	if _, _, e = VerifyImage(dir); e == nil {
		t.Fatal("accepted invalid artifact digest")
	}
}

func TestStrictMetadata(t *testing.T) {
	for _, raw := range []string{`{"unknown":true}`, `{} {}`, `{} trailing`} {
		if e := strictJSON([]byte(raw), new(Image)); e == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestUncertainLaunchRetainsVM(t *testing.T) {
	dir := t.TempDir()
	me, e := identity(os.Getpid())
	if e != nil {
		t.Fatal(e)
	}
	// A mismatched start time is a dead/reused helper identity.
	me.Start += "0"
	if e = writeState(dir, runtimeState{Helper: me, Dir: "/tmp/yfc-fixture", LaunchPending: true}); e != nil {
		t.Fatal(e)
	}
	if _, e = running(dir); e == nil {
		t.Fatal("uncertain launch considered safely stopped")
	}
}

func TestMalformedVMIdentityDoesNotProveTermination(t *testing.T) {
	dir := t.TempDir()
	me, e := identity(os.Getpid())
	if e != nil {
		t.Fatal(e)
	}
	for _, invalid := range []process{{PID: -1}, {PID: 42}, {Start: "42"}, {PID: 42, Start: "invalid"}} {
		if e = writeState(dir, runtimeState{Helper: me, VM: invalid, Dir: "/tmp/yfc-fixture"}); e != nil {
			t.Fatal(e)
		}
		if _, e = running(dir); e == nil {
			t.Fatalf("accepted malformed VM identity: %+v", invalid)
		}
	}
}
