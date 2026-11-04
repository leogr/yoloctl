package openshell

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestOperatorRegistrationRootAbsenceUsesLiteralPrefixWithoutIncarnation(t *testing.T) {
	base, anchor, observe := publicSSHColdFixture(t, publicSSHColdStamp())
	p, err := snapshotOperatorRegistrationRootAbsence(t.Context(), anchor.open, os.Geteuid(), os.Getegid(), publicSSHCurrentOK, publicSSHCurrentOK, observe)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	if p.original.box != "operators" {
		t.Fatal("root absence invented a selected incarnation")
	}
	syncs := 0
	p.original.sync = func(fd int) error { syncs++; return unix.Fsync(fd) }
	r, err := p.original.withdraw(t.Context(), publicSSHCurrentOK, observe)
	if err != nil || !r.ObservedDirectoryAbsent || !r.ParentSynced || syncs != 1 {
		t.Fatal("literal prefix absence/fresh sync not established", r, err)
	}
	if _, err := os.Lstat(filepath.Join(base, "operators")); !os.IsNotExist(err) {
		t.Fatal("absent prefix created or adopted", err)
	}
	if _, err := p.original.withdraw(t.Context(), publicSSHCurrentOK, observe); err == nil || syncs != 1 {
		t.Fatal("literal root absence replayed")
	}
}

func TestOperatorRegistrationRootAbsenceRefusesPresentOrPartialPrefix(t *testing.T) {
	for _, kind := range []string{"directory", "file", "link"} {
		t.Run(kind, func(t *testing.T) {
			base, anchor, observe := publicSSHColdFixture(t, publicSSHColdStamp())
			name := filepath.Join(base, "operators")
			var err error
			switch kind {
			case "directory":
				err = os.Mkdir(name, 0o755)
			case "file":
				err = os.WriteFile(name, nil, 0o600)
			case "link":
				err = os.Symlink("missing", name)
			}
			if err != nil {
				t.Fatal(err)
			}
			opens := 0
			open := func() (*os.File, error) { opens++; return anchor.open() }
			p, err := snapshotOperatorRegistrationRootAbsence(t.Context(), open, os.Geteuid(), os.Getegid(), publicSSHCurrentOK, publicSSHCurrentOK, observe)
			if err == nil || p != nil || opens == 0 || !strings.Contains(err.Error(), "fixed directory absence") {
				t.Fatal("present operator prefix skipped complete roster", err)
			}
		})
	}
}

func TestOperatorRegistrationRootAbsenceDoesNotRelaxSSHNamesOrAcceptPaths(t *testing.T) {
	_, anchor, observe := publicSSHColdFixture(t, publicSSHColdStamp())
	opens := 0
	open := func() (*os.File, error) { opens++; return anchor.open() }
	if got, err := snapshotPublicSSHDirectoryAbsence(t.Context(), open, "operators", os.Geteuid(), os.Getegid(), publicSSHCurrentOK, publicSSHCurrentOK, observe); err == nil || got != nil || opens != 0 {
		t.Fatal("operator-root support relaxed SSH reserved box refusal", err)
	}
	for _, child := range []string{"", ".", "..", "../operators", "/operators", "operators/other", "sessions", "controller", "operator"} {
		if got, err := snapshotFixedPublicDirectoryAbsence(t.Context(), open, child, os.Geteuid(), os.Getegid(), publicSSHCurrentOK, publicSSHCurrentOK, observe); err == nil || got != nil || opens != 0 {
			t.Fatal("fixed-child core accepted invalid/reserved path", child, err)
		}
	}
}

func TestOperatorRegistrationRootAbsenceRequiresActualNativeAndInitialInactive(t *testing.T) {
	_, anchor, observe := publicSSHColdFixture(t, publicSSHColdStamp())
	boom := errors.New("initial inactive proof unavailable")
	if p, err := snapshotOperatorRegistrationRootAbsence(t.Context(), anchor.open, os.Geteuid(), os.Getegid(), publicSSHCurrentOK, func(context.Context) error { return boom }, observe); !errors.Is(err, boom) || p != nil {
		t.Fatal("initial inactivity was inferred", err)
	}
	if p, err := PrepareOperatorRegistrationRootAbsence(t.Context(), publicSSHCurrentOK, publicSSHCurrentOK); err == nil || p != nil {
		t.Fatal("ordinary executor entered native operator absence")
	}
	if _, err := new(PreparedOperatorRegistrationRootAbsence).Withdraw(t.Context(), publicSSHCurrentOK); err == nil {
		t.Fatal("zero root observation became success")
	}
}
