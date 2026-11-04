package operator

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func endpointCleanupFixture(t *testing.T) (string, Endpoint) {
	t.Helper()
	root := t.TempDir()
	e := Endpoint{Box: "box", Incarnation: strings.Repeat("a", 32)}
	dir := filepath.Join(root, e.Incarnation)
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(e)
	for name, body := range map[string][]byte{"identity.json": raw, "client.json": []byte(`{"space":"fixture","generation":"fixture"}`)} {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root, e
}
func TestRemoveOwnedEndpointPreservesOtherRegistrations(t *testing.T) {
	root, e := endpointCleanupFixture(t)
	other := filepath.Join(root, strings.Repeat("b", 32))
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}
	// Short Unix path keeps the project regression portable across fixtures.
	sock := filepath.Join(t.TempDir(), "s")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	target := filepath.Join(root, e.Incarnation, "control.sock")
	if err := os.Rename(sock, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := removeEndpointAt(root, e, os.Geteuid(), os.Getegid()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("other registration affected", err)
	}
	if _, err := os.Lstat(filepath.Join(root, e.Incarnation)); !os.IsNotExist(err) {
		t.Fatal("own registration retained", err)
	}
	if err := removeEndpointAt(root, e, os.Geteuid(), os.Getegid()); err == nil {
		t.Fatal("absence substituted for removal acknowledgement")
	}
}
func TestRemoveOwnedEndpointRefusesForeignAndUntrustedMembers(t *testing.T) {
	for _, change := range []string{"identity", "symlink", "extra", "mode", "directory-link"} {
		t.Run(change, func(t *testing.T) {
			root, e := endpointCleanupFixture(t)
			dir := filepath.Join(root, e.Incarnation)
			switch change {
			case "identity":
				if err := os.WriteFile(filepath.Join(dir, "identity.json"), []byte(`{}`), 0o644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Remove(filepath.Join(dir, "client.json")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("identity.json", filepath.Join(dir, "client.json")); err != nil {
					t.Fatal(err)
				}
			case "extra":
				if err := os.WriteFile(filepath.Join(dir, "unknown"), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			case "mode":
				if err := os.Chmod(filepath.Join(dir, "client.json"), 0o666); err != nil {
					t.Fatal(err)
				}
			case "directory-link":
				if err := os.Rename(dir, dir+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(dir+"-old", dir); err != nil {
					t.Fatal(err)
				}
			}
			if err := removeEndpointAt(root, e, os.Geteuid(), os.Getegid()); err == nil {
				t.Fatal("foreign cleanup accepted")
			}
			if _, err := os.Lstat(filepath.Join(dir, "identity.json")); err != nil {
				t.Fatal("refusal deleted metadata", err)
			}
		})
	}
}
func TestRemoveEndpointRefusesOrdinaryExecutor(t *testing.T) {
	if err := RemoveEndpoint(Endpoint{Box: "box", Incarnation: strings.Repeat("a", 32)}); err == nil {
		t.Fatal("ordinary cleanup accepted")
	}
}
