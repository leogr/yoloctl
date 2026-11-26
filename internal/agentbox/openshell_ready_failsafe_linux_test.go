package agentbox

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"os"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// The native wait for the box's SSH endpoint is ended by its caller or by the
// fail-safe, which is at least a minute: box attach's owned setup can outlive
// its client, and there the fail-safe is the only bound.
func TestNativeCreatedSSHWaitUsesFailSafe(t *testing.T) {
	key, err := ssh.NewPublicKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{1}, 32)).Public())
	if err != nil {
		t.Fatal(err)
	}
	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = directory.Close() }()
	checked := errors.New("bound checked")
	err = waitNativeCreatedSSH(t.Context(), directory, string(ssh.MarshalAuthorizedKey(key)), func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) < time.Minute || time.Until(deadline) > SandboxReadyFailSafe {
			t.Fatal("native readiness wait is not bounded by its fail-safe")
		}
		return checked
	})
	if !errors.Is(err, checked) {
		t.Fatal(err)
	}
	caller, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	want, _ := caller.Deadline()
	err = waitNativeCreatedSSH(caller, directory, string(ssh.MarshalAuthorizedKey(key)), func(ctx context.Context) error {
		if deadline, ok := ctx.Deadline(); !ok || !deadline.Equal(want) {
			t.Fatal("native readiness wait replaced an earlier caller deadline")
		}
		return checked
	})
	if !errors.Is(err, checked) {
		t.Fatal(err)
	}
}
