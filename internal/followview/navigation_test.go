package followview

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/leogr/yoloctl/internal/filelink"
)

func TestLinkedReadersRejectReplacementAndRevokedGrants(t *testing.T) {
	root := fixtureDir(t)
	other := fixtureDir(t)
	path := filepath.Join(root, "a.md")
	outside := filepath.Join(other, "secret")
	writeFixture(t, path, []byte("inside"))
	writeFixture(t, outside, []byte("outside"))
	for _, mode := range []string{"host", "vm"} {
		target := filelink.Target{Mode: mode, Path: path, Root: root}
		var probe filelink.Probe = HostProbe
		grants := []string{root}
		vm := pythonReader(path, func() []string { return grants })
		vm.Root = root
		var reader Reader = HostReader{Path: path, Root: root}
		if mode == "vm" {
			target.Space = "dev"
			probe = vm.Probe
			reader = vm
		}
		if e := probe(context.Background(), []filelink.Target{target})[target]; e != nil {
			t.Fatal(mode, e)
		}
		if e := os.Rename(path, path+".saved"); e != nil {
			t.Fatal(e)
		}
		if e := os.Symlink(outside, path); e != nil {
			t.Fatal(e)
		}
		if e := probe(context.Background(), []filelink.Target{target})[target]; e == nil {
			t.Fatal(mode, "symlink admitted")
		}
		if _, e := reader.Read(context.Background(), Query{}); e == nil {
			t.Fatal(mode, "replacement read escaped root")
		}
		// Ordinary explicit host follow retains its existing symlink behavior.
		if mode == "host" {
			s, e := (HostReader{Path: path}).Read(context.Background(), Query{})
			if e != nil || string(s.Data) != "outside" {
				t.Fatal(s, e)
			}
		}
		if e := os.Remove(path); e != nil {
			t.Fatal(e)
		}
		if e := os.Rename(path+".saved", path); e != nil {
			t.Fatal(e)
		}
		if mode == "vm" {
			grants = nil
			if _, e := reader.Read(context.Background(), Query{}); e == nil {
				t.Fatal("revoked grant read")
			}
		}
	}
	for _, path := range []string{root, filepath.Join(root, "missing")} {
		target := filelink.Target{Mode: "host", Root: root, Path: path}
		if HostProbe(context.Background(), []filelink.Target{target})[target] == nil {
			t.Fatal("nonregular/missing admitted")
		}
	}
}

func TestVMProbeKeepsOtherGrantedTargetsWhenFirstWasRevoked(t *testing.T) {
	root := fixtureDir(t)
	left, right := filepath.Join(root, "left"), filepath.Join(root, "right")
	for _, p := range []string{left, right} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
		writeFixture(t, filepath.Join(p, "a.md"), []byte("file"))
	}
	first := filelink.Target{Mode: "vm", Space: "dev", Root: root, Path: filepath.Join(left, "a.md")}
	second := first
	second.Path = filepath.Join(right, "a.md")
	reader := pythonReader(first.Path, func() []string { return []string{right} })
	got := reader.Probe(context.Background(), []filelink.Target{first, second})
	if got[first] == nil || got[second] != nil {
		t.Fatalf("mixed current grant results: %v", got)
	}
}
