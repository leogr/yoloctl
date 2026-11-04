package nativerelease

import (
	"os/exec"
	"testing"
)

func TestInstallerPreservesWorkingBinaryOnInvalidRelease(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	cmd := exec.Command(python, "-I", "install_test.py")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("installer regression: %v\n%s", err, out)
	}
}

func TestReleaseArchitectures(t *testing.T) {
	r, err := Lookup("antigravity")
	if err != nil {
		t.Fatal(err)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		if _, err := r.ManifestURL(arch); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.ManifestURL("386"); err == nil {
		t.Fatal("unsupported architecture accepted")
	}
	if _, err := Lookup("arbitrary-command"); err == nil {
		t.Fatal("unknown native updater accepted")
	}
}
