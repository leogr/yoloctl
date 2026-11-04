package operator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func stubNativeInfrastructure(t *testing.T) string {
	t.Helper()
	root := identityFixtureRoot(t)
	old := publishNativeInfrastructure
	publishNativeInfrastructure = func() (func() (*os.File, error), int, int, error) {
		return func() (*os.File, error) { return openEndpointDirectory(unix.AT_FDCWD, root, false) }, os.Geteuid(), os.Getegid(), nil
	}
	t.Cleanup(func() { publishNativeInfrastructure = old })
	return root
}

// A native identity is published exclusively and atomically under the fixed
// root, with its admission checked; the same registration is never adopted
// twice, and closing it leaves it published.
func TestPublishNativeIdentityPublishesExclusively(t *testing.T) {
	root := stubNativeInfrastructure(t)
	identity := taggedNativeIdentity(Endpoint{Box: "box", Incarnation: strings.Repeat("a", 32)})
	r, err := PublishNativeIdentity(t.Context(), identity, endpointCurrentOK)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, identity.Incarnation, "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRegistrationIdentity(raw)
	if err != nil || got != identity {
		t.Fatalf("published %+v (%v), want %+v", got, err, identity)
	}
	if again, err := PublishNativeIdentity(t.Context(), identity, endpointCurrentOK); err == nil {
		_ = again.Close()
		t.Fatal("an existing registration was published again")
	} else if again != nil {
		_ = again.Close()
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, identity.Incarnation, "identity.json")); err != nil {
		t.Fatalf("closing unlinked the registration: %v", err)
	}
	if r.String() != "<native operator registration>" {
		t.Fatal("the registration prints its contents")
	}
}

// A legacy identity, a missing admission or a refused admission publishes
// nothing.
func TestPublishNativeIdentityRefusesWithoutNativeAdmission(t *testing.T) {
	root := stubNativeInfrastructure(t)
	endpoint := Endpoint{Box: "box", Incarnation: strings.Repeat("b", 32)}
	if r, err := PublishNativeIdentity(t.Context(), LegacyRegistrationIdentity(endpoint), endpointCurrentOK); err == nil || r != nil {
		t.Fatal("a legacy identity was published natively")
	}
	if r, err := PublishNativeIdentity(t.Context(), taggedNativeIdentity(endpoint), nil); err == nil || r != nil {
		t.Fatal("published without its admission")
	}
	denied := errors.New("denied")
	r, err := PublishNativeIdentity(t.Context(), taggedNativeIdentity(endpoint), func(context.Context) error { return denied })
	if !errors.Is(err, denied) {
		t.Fatalf("refused admission: %v", err)
	}
	if r != nil {
		_ = r.Close()
	}
	if _, err := os.Stat(filepath.Join(root, endpoint.Incarnation)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused admission created the registration: %v", err)
	}
}

// Controller startup after a VM reboot (registration erased) publishes the
// box's native identity and routing record again; after a controller restart
// (registration kept) it keeps the identical identity and rewrites the routing
// record; a different existing identity is refused and left unchanged.
func TestRestoreNativeAfterRebootAndRestart(t *testing.T) {
	root := stubNativeInfrastructure(t)
	identity := taggedNativeIdentity(Endpoint{Box: "box", Incarnation: strings.Repeat("c", 32)})
	routing := Client{Space: "space", Generation: "1"}
	for _, pass := range []string{"reboot", "restart"} {
		if pass == "restart" {
			if err := os.Remove(filepath.Join(root, identity.Incarnation, "client.json")); err != nil {
				t.Fatal(err)
			}
		}
		if err := restoreNativeAt(t.Context(), root, identity, routing); err != nil {
			t.Fatalf("%s: %v", pass, err)
		}
		raw, err := os.ReadFile(filepath.Join(root, identity.Incarnation, "identity.json"))
		if err != nil {
			t.Fatal(err)
		}
		if got, err := DecodeRegistrationIdentity(raw); err != nil || got != identity {
			t.Fatalf("%s: identity %+v (%v)", pass, got, err)
		}
		if _, err := os.Stat(filepath.Join(root, identity.Incarnation, "client.json")); err != nil {
			t.Fatalf("%s: routing record: %v", pass, err)
		}
	}
	other := identity
	other.Box = "other"
	if err := restoreNativeAt(t.Context(), root, other, routing); err == nil {
		t.Fatal("a different existing identity was accepted")
	}
	if err := restoreNativeAt(t.Context(), root, LegacyRegistrationIdentity(identity.Endpoint), routing); err == nil {
		t.Fatal("a legacy identity was restored natively")
	}
}
