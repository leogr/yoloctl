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

func nativePublicationCustodyFixture(t *testing.T) (string, *nativeRegistrationCustody) {
	t.Helper()
	root := identityFixtureRoot(t)
	open := func() (*os.File, error) { return openEndpointDirectory(unix.AT_FDCWD, root, false) }
	identity := taggedNativeIdentity(Endpoint{Box: "box", Incarnation: strings.Repeat("a", 32)})
	p, err := createNativeRegistrationCustody(t.Context(), open, identity, os.Geteuid(), os.Getegid(), endpointCurrentOK)
	if p != nil {
		t.Cleanup(func() { _ = p.Close() })
	}
	if err != nil {
		t.Fatal(err)
	}
	return root, p
}

func TestNativePublicationCustodyRetainsOriginalIdentityFDAcrossRename(t *testing.T) {
	root, p := nativePublicationCustodyFixture(t)
	called := 0
	var original endpointFileID
	p.rename = func(fd int, from, to string) error {
		called++
		var err error
		original, err = endpointID(int(p.file.Fd()), "")
		if err != nil {
			return err
		}
		if p.fileID != original || from != p.staging || to != "identity.json" {
			t.Fatal("publication did not retain original staging inode")
		}
		return renameLegacyIdentityNoReplace(fd, from, to)
	}
	if err := p.publishIdentity(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !p.created || !p.published || called != 1 || p.staging != "" || original.dev != p.fileID.dev || original.ino != p.fileID.ino {
		t.Fatal("original publication custody changed")
	}
	if err := p.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := p.publishIdentity(t.Context()); err == nil || called != 1 {
		t.Fatal("publication replayed")
	}
	path := filepath.Join(root, p.identity.Incarnation, "identity.json")
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != string(p.raw) {
		t.Fatal("published bytes differ", err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.Check(t.Context()); err == nil {
		t.Fatal("closed custody revived")
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatal("Close silently unlinked original publication", err)
	}
}

func TestNativePublicationCustodyNeverAdoptsExistingDirectory(t *testing.T) {
	root, p := nativePublicationCustodyFixture(t)
	if err := p.publishIdentity(t.Context()); err != nil {
		t.Fatal(err)
	}
	second, err := createNativeRegistrationCustody(t.Context(), func() (*os.File, error) { return openEndpointDirectory(unix.AT_FDCWD, root, false) }, p.identity, os.Geteuid(), os.Getegid(), endpointCurrentOK)
	if second != nil {
		defer func() { _ = second.Close() }()
	}
	if !errors.Is(err, os.ErrExist) || second == nil || second.created || second.directory != nil {
		t.Fatal("existing directory reconstructed original custody", err)
	}
	if err := second.Check(t.Context()); err == nil {
		t.Fatal("failed exclusive creation became usable")
	}
	if err := p.Check(t.Context()); err != nil {
		t.Fatal("failed competitor changed original", err)
	}
}

func TestNativePublicationCustodyRefusesReplacementBeforeAnyIdentityEffect(t *testing.T) {
	for _, replace := range []string{"directory", "root"} {
		t.Run(replace, func(t *testing.T) {
			root, p := nativePublicationCustodyFixture(t)
			path := filepath.Join(root, p.identity.Incarnation)
			if replace == "root" {
				path = root
			}
			moved := path + "-original"
			if err := os.Rename(path, moved); err != nil {
				t.Fatal(err)
			}
			// Keep test teardown in the testing-owned tree for renamed roots.
			t.Cleanup(func() { _ = os.Rename(moved, path) })
			if err := os.Mkdir(path, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := p.publishIdentity(t.Context()); err == nil {
				t.Fatal("replaced original mapping accepted")
			}
			if p.publishTried || p.file != nil {
				t.Fatal("replacement reached an identity effect")
			}
			entries, err := os.ReadDir(path)
			if err != nil || len(entries) != 0 {
				t.Fatal("foreign replacement mutated", entries, err)
			}
		})
	}
}

func TestNativePublicationCustodyPublicationFailureRetainsOriginalPartialState(t *testing.T) {
	root, p := nativePublicationCustodyFixture(t)
	failure := errors.New("original rename failed")
	calls := 0
	p.rename = func(int, string, string) error { calls++; return failure }
	if err := p.publishIdentity(t.Context()); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if p.file == nil || p.staging == "" || p.published || p.failure == nil {
		t.Fatal("partial original state discarded")
	}
	path := filepath.Join(root, p.identity.Incarnation, p.staging)
	if _, err := os.Lstat(path); err != nil {
		t.Fatal("failed publication erased evidence", err)
	}
	if err := p.publishIdentity(t.Context()); !errors.Is(err, failure) || calls != 1 {
		t.Fatal("failed effect retried/reinterpreted", err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatal("Close erased partial evidence", err)
	}
}

func TestNativePublicationCustodyDetectsMatchingBytesOnSubstitutedIdentity(t *testing.T) {
	root, p := nativePublicationCustodyFixture(t)
	if err := p.publishIdentity(t.Context()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, p.identity.Incarnation, "identity.json")
	if err := os.Rename(path, filepath.Join(root, "retained-original-identity")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, p.raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := p.Check(t.Context()); err == nil {
		t.Fatal("matching replacement bytes adopted")
	}
}

func TestNativePublicationCustodyRefusesReusedOriginalFDAtPublicationCut(t *testing.T) {
	root, p := nativePublicationCustodyFixture(t)
	var reused = -1
	t.Cleanup(func() {
		if reused >= 0 {
			_ = unix.Close(reused)
		}
	})
	p.rename = func(directory int, from, to string) error {
		if err := renameLegacyIdentityNoReplace(directory, from, to); err != nil {
			return err
		}
		originalFD := int(p.file.Fd())
		var err error
		reused, err = unix.Open(filepath.Join(root, "foreign-file"), unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC, 0o644)
		if err != nil {
			return err
		}
		// Both slots belong exclusively to this fixture. Atomic replacement
		// never exposes a free slot another runtime goroutine could acquire.
		return unix.Dup3(reused, originalFD, unix.O_CLOEXEC)
	}
	if err := p.publishIdentity(t.Context()); err == nil {
		t.Fatal("reused FD adopted as original publication")
	}
	if p.failure == nil {
		t.Fatal("lost original file custody was not retained as failure")
	}
}

func TestNativePublicationCustodyCurrentAdmissionAndCancellationPrecedeEffects(t *testing.T) {
	for _, phase := range []string{"admission", "cancellation"} {
		t.Run(phase, func(t *testing.T) {
			root, p := nativePublicationCustodyFixture(t)
			ctx := t.Context()
			if phase == "admission" {
				p.verify = func(context.Context) error { return errors.New("original admission expired") }
			} else {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if err := p.publishIdentity(ctx); err == nil || p.publishTried {
				t.Fatal("failed preflight reached an identity effect", err)
			}
			p.verify = endpointCurrentOK
			if err := p.publishIdentity(t.Context()); err == nil {
				t.Fatal("failed original admission was silently revived")
			}
			entries, err := os.ReadDir(filepath.Join(root, p.identity.Incarnation))
			if err != nil || len(entries) != 0 {
				t.Fatal("failed preflight changed directory", err)
			}
		})
	}
}
