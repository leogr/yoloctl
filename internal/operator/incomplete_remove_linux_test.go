package operator

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// The independently selected identity is real fixture metadata. No fixture
// callback stands for a production publisher/consumer or Stop/rm ACK.
func incompleteEndpointFixture(t *testing.T, socket bool) (string, Endpoint) {
	t.Helper()
	root, e := endpointCleanupFixture(t)
	for path, mode := range map[string]os.FileMode{
		root: 0o700, eDirectory(root, e): 0o755,
		filepath.Join(eDirectory(root, e), "identity.json"): 0o644,
	} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(filepath.Join(eDirectory(root, e), "client.json")); err != nil {
		t.Fatal(err)
	}
	if socket {
		short, err := os.MkdirTemp("", "ope-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(short) })
		l, err := net.ListenUnix("unix", &net.UnixAddr{Net: "unix", Name: filepath.Join(short, "s")})
		if err != nil {
			t.Fatal(err)
		}
		l.SetUnlinkOnClose(false)
		t.Cleanup(func() { _ = l.Close() })
		name := filepath.Join(eDirectory(root, e), "control.sock")
		if err := os.Rename(l.Addr().String(), name); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(name, 0o666); err != nil {
			t.Fatal(err)
		}
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return root, e
}

func eDirectory(root string, e Endpoint) string { return filepath.Join(root, e.Incarnation) }

func TestIncompleteEndpointRemovalOriginalProfilesAndOrderedEffects(t *testing.T) {
	for _, socket := range []bool{false, true} {
		t.Run(map[bool]string{false: "identity-only", true: "identity-and-socket"}[socket], func(t *testing.T) {
			root, e := incompleteEndpointFixture(t, socket)
			if p, err := prepareEndpointRemovalAt(t.Context(), root, e, os.Geteuid(), os.Getegid(), endpointCurrentOK); err == nil {
				_ = p.Close()
				t.Fatal("complete profile accepted missing routing metadata")
			}
			ownedChecks := 0
			p, err := prepareIncompleteEndpointRemovalAt(t.Context(), root, e, os.Geteuid(), os.Getegid(), func(context.Context) error { ownedChecks++; return nil })
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = p.Close() }()
			if ownedChecks < 2 {
				t.Fatal("initial independent owner was not bracketed")
			}
			other := filepath.Join(root, strings.Repeat("b", 32))
			if err := os.Mkdir(other, 0o755); err != nil {
				t.Fatal(err)
			}
			var effects []string
			p.unlink = func(fd int, name string, flags int) error {
				effects = append(effects, "unlink:"+name)
				return unix.Unlinkat(fd, name, flags)
			}
			p.sync = func(fd int) error { effects = append(effects, "sync"); return unix.Fsync(fd) }
			checks := 0
			if err := p.withdraw(t.Context(), func(context.Context) error { checks++; return nil }); err != nil {
				t.Fatal(err)
			}
			want := "unlink:identity.json,sync,unlink:" + e.Incarnation + ",sync"
			if socket {
				want = "unlink:control.sock,sync," + want
			}
			if strings.Join(effects, ",") != want || checks != 2+map[bool]int{false: 0, true: 1}[socket] {
				t.Fatalf("wrong ACK order/current checks: %v/%d", effects, checks)
			}
			if _, err := os.Lstat(eDirectory(root, e)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("original registration retained", err)
			}
			if _, err := os.Stat(other); err != nil {
				t.Fatal("sibling affected", err)
			}
			before := len(effects)
			if err := p.withdraw(t.Context(), endpointCurrentOK); err == nil || len(effects) != before {
				t.Fatal("completed partial withdrawal replayed")
			}
		})
	}
}

func TestIncompleteEndpointRemovalRequiresDisjointOriginalProfile(t *testing.T) {
	root, e := incompleteEndpointFixture(t, false)
	client := filepath.Join(eDirectory(root, e), "client.json")
	if err := os.WriteFile(client, []byte(`{"space":"s","generation":"g"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if p, err := prepareIncompleteEndpointRemovalAt(t.Context(), root, e, os.Geteuid(), os.Getegid(), endpointCurrentOK); err == nil {
		_ = p.Close()
		t.Fatal("incomplete profile adopted originally complete registration")
	}
	p, err := prepareEndpointRemovalAt(t.Context(), root, e, os.Geteuid(), os.Getegid(), endpointCurrentOK)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	if err := os.Remove(client); err != nil {
		t.Fatal(err)
	}
	effects := 0
	p.unlink = func(int, string, int) error { effects++; return nil }
	if err := p.withdraw(t.Context(), endpointCurrentOK); err == nil || effects != 0 {
		t.Fatal("complete observation switched after routing loss")
	}
	if p, err := prepareEndpointProfileAt(t.Context(), root, e, os.Geteuid(), os.Getegid(), endpointCurrentOK, 0); err == nil {
		_ = p.Close()
		t.Fatal("zero profile accepted")
	}
}

func TestIncompleteEndpointRemovalRetainsOriginalMembership(t *testing.T) {
	for _, change := range []string{"client-added", "identity-replaced", "identity-missing", "extra", "directory-replaced", "owner-refused"} {
		t.Run(change, func(t *testing.T) {
			root, e := incompleteEndpointFixture(t, false)
			p, err := prepareIncompleteEndpointRemovalAt(t.Context(), root, e, os.Geteuid(), os.Getegid(), endpointCurrentOK)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = p.Close() }()
			dir := eDirectory(root, e)
			current := endpointCurrentOK
			switch change {
			case "client-added":
				err = os.WriteFile(filepath.Join(dir, "client.json"), []byte("{}"), 0o644)
			case "identity-replaced":
				raw := append([]byte(nil), p.raw["identity.json"]...)
				err = os.Rename(filepath.Join(dir, "identity.json"), filepath.Join(dir, "old"))
				if err == nil {
					err = os.WriteFile(filepath.Join(dir, "identity.json"), raw, 0o644)
				}
				if err == nil {
					err = os.Remove(filepath.Join(dir, "old"))
				}
			case "identity-missing":
				err = os.Remove(filepath.Join(dir, "identity.json"))
			case "extra":
				err = os.WriteFile(filepath.Join(dir, "foreign"), nil, 0o644)
			case "directory-replaced":
				err = os.Rename(dir, dir+"-old")
				if err == nil {
					err = os.Mkdir(dir, 0o755)
				}
			case "owner-refused":
				current = func(context.Context) error { return errors.New("independent owner/consumer refused") }
			}
			if err != nil {
				t.Fatal(err)
			}
			effects := 0
			p.unlink = func(int, string, int) error { effects++; return nil }
			if err := p.withdraw(t.Context(), current); err == nil || effects != 0 {
				t.Fatal("changed original accepted", effects, err)
			}
			if err := p.withdraw(t.Context(), endpointCurrentOK); err == nil || effects != 0 {
				t.Fatal("failed attempt replayed")
			}
		})
	}
}

func TestIncompleteEndpointRemovalSyncFailureIsConsumed(t *testing.T) {
	root, e := incompleteEndpointFixture(t, false)
	p, err := prepareIncompleteEndpointRemovalAt(t.Context(), root, e, os.Geteuid(), os.Getegid(), endpointCurrentOK)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	boom := errors.New("original directory sync failed")
	effects := 0
	p.sync = func(int) error { effects++; return boom }
	if err := p.withdraw(t.Context(), endpointCurrentOK); !errors.Is(err, boom) {
		t.Fatal("failed actual sync hidden", err)
	}
	if _, err := os.Lstat(filepath.Join(eDirectory(root, e), "identity.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("expected original unlink did not occur", err)
	}
	if err := p.withdraw(t.Context(), endpointCurrentOK); err == nil || effects != 1 {
		t.Fatal("uncertain partial suffix replayed")
	}
	if _, err := os.Stat(eDirectory(root, e)); err != nil {
		t.Fatal("directory removed after failed sync", err)
	}
}

func TestIncompleteEndpointRemovalRequiresTrustedInitialIdentity(t *testing.T) {
	for _, change := range []string{"missing-identity", "wrong-identity", "linked-identity", "writable-directory", "unexpected-member"} {
		t.Run(change, func(t *testing.T) {
			root, e := incompleteEndpointFixture(t, false)
			dir := eDirectory(root, e)
			identity := filepath.Join(dir, "identity.json")
			var err error
			switch change {
			case "missing-identity":
				err = os.Remove(identity)
			case "wrong-identity":
				err = os.WriteFile(identity, []byte(`{"box":"other","incarnation":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`), 0o644)
			case "linked-identity":
				err = os.Rename(identity, filepath.Join(root, "saved"))
				if err == nil {
					err = os.Symlink(filepath.Join(root, "saved"), identity)
				}
			case "writable-directory":
				err = os.Chmod(dir, 0o777)
			case "unexpected-member":
				err = os.WriteFile(filepath.Join(dir, "foreign"), nil, 0o644)
			}
			if err != nil {
				t.Fatal(err)
			}
			p, err := prepareIncompleteEndpointRemovalAt(t.Context(), root, e, os.Geteuid(), os.Getegid(), endpointCurrentOK)
			if err == nil {
				_ = p.Close()
				t.Fatal("untrusted initial partial identity accepted")
			}
		})
	}
}
