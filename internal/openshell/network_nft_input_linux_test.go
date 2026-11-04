package openshell

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func nativeNFTInputFixture(t *testing.T, body string) *os.File {
	t.Helper()
	name := filepath.Join(t.TempDir(), "nft-fixture")
	if err := os.WriteFile(name, []byte("#!/bin/sh\nset -eu\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func TestNativeNFTScriptUsesActualInputDescriptor(t *testing.T) {
	// Emulate nft's filename reader while explicitly rejecting the broken
	// '-' spelling. Exercise the real inherited executable and stdin pipe.
	f := nativeNFTInputFixture(t, `[ "$#" -eq 2 ] && [ "$1" = '-f' ] && [ "$2" = '/proc/self/fd/0' ] || { echo 'stdin alias unavailable' >&2; exit 19; }
cat "$2"
`)
	args := []string{"-f", "-"}
	policy := "table inet yoloctl_fixture {\n}\n"
	out, err := runNativeNFT(t.Context(), f, args, strings.NewReader(policy))
	if err != nil || string(out) != policy {
		t.Fatalf("script input changed: %q %v", out, err)
	}
	if !slices.Equal(args, []string{"-f", "-"}) {
		t.Fatal("transport changed caller's retained plan arguments")
	}
}

func TestNativeNFTReadbackArgumentsRemainUnchanged(t *testing.T) {
	f := nativeNFTInputFixture(t, "printf '%s\\n' \"$@\"\n")
	args := []string{"-j", "list", "table", "inet", EgressTable}
	out, err := runNativeNFT(t.Context(), f, args, nil)
	if err != nil || string(out) != strings.Join(args, "\n")+"\n" {
		t.Fatalf("readback changed: %q %v", out, err)
	}
}

func TestNativeNFTScriptFailurePreservesOriginalError(t *testing.T) {
	f := nativeNFTInputFixture(t, `[ "$2" = '/proc/self/fd/0' ] || exit 19
cat "$2" >/dev/null
echo 'original nft rejection' >&2
exit 23
`)
	out, err := runNativeNFT(t.Context(), f, []string{"-f", "-"}, strings.NewReader("invalid rules"))
	if out != nil || err == nil || !strings.Contains(err.Error(), "exit status 23") || !strings.Contains(err.Error(), "original nft rejection") {
		t.Fatalf("failed ACK changed: %q %v", out, err)
	}
}
