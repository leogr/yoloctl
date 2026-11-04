package openshell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func publicSSHColdStamp() publicSSHKernelStamp {
	return publicSSHKernelStamp{"11111111-2222-4333-8444-555555555555", namespaceIdentity{1, 2}, namespaceIdentity{1, 3}}
}

func publicSSHColdFixture(t *testing.T, stamp publicSSHKernelStamp) (string, *PreparedPublicSSHDirectoryAbsence, func(context.Context) (publicSSHKernelStamp, error)) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	observe := func(context.Context) (publicSSHKernelStamp, error) { return stamp, nil }
	p, err := snapshotPublicSSHDirectoryAbsence(t.Context(), func() (*os.File, error) {
		fd, err := unix.Openat2(unix.AT_FDCWD, root, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS})
		if err != nil {
			return nil, err
		}
		return os.NewFile(uintptr(fd), root), nil
	}, "box", os.Geteuid(), os.Getegid(), publicSSHCurrentOK, publicSSHCurrentOK, observe)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return root, p, observe
}

func TestPublicSSHColdAbsenceSyncsOriginalParentWithoutInventingUnlink(t *testing.T) {
	root, p, observe := publicSSHColdFixture(t, publicSSHColdStamp())
	syncs := 0
	p.sync = func(fd int) error { syncs++; return unix.Fsync(fd) }
	got, err := p.withdraw(t.Context(), publicSSHCurrentOK, observe)
	if err != nil || got != (PublicSSHDirectoryAbsenceResult{true, true}) || syncs != 1 {
		t.Fatal("cold current absence/fresh sync was not established", got, err, syncs)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("cold observer created or adopted a directory", err, entries)
	}
	if _, err := p.withdraw(t.Context(), publicSSHCurrentOK, observe); err == nil || syncs != 1 {
		t.Fatal("completed cold observation replayed")
	}
}

func TestPublicSSHColdAbsenceRejectsChangedScopeBeforeSync(t *testing.T) {
	for _, change := range []string{"directory", "file", "dangling-link", "parent-replaced", "parent-missing", "parent-mode", "parent-mount", "boot", "mount-namespace", "network-namespace", "kernel-error", "lookup-error", "mixed-enoent", "same-boot-consumer", "other-current-boot-consumer"} {
		t.Run(change, func(t *testing.T) {
			stamp := publicSSHColdStamp()
			if change == "other-current-boot-consumer" {
				// Capture a different CURRENT boot normally. No historical boot
				// relation is supplied or inferred; current users still refuse.
				stamp.boot = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
			}
			root, p, observe := publicSSHColdFixture(t, stamp)
			boom := errors.New("current proof unavailable")
			current := publicSSHCurrentOK
			switch change {
			case "directory":
				if err := os.Mkdir(filepath.Join(root, "box"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "file":
				if err := os.WriteFile(filepath.Join(root, "box"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			case "dangling-link":
				if err := os.Symlink("missing", filepath.Join(root, "box")); err != nil {
					t.Fatal(err)
				}
			case "parent-replaced", "parent-missing":
				if err := os.Rename(root, root+"-old"); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Rename(root+"-old", root) })
				if change == "parent-replaced" {
					if err := os.Mkdir(root, 0o700); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = os.Remove(root) })
				}
			case "parent-mode":
				if err := os.Chmod(root, 0o777); err != nil {
					t.Fatal(err)
				}
			case "parent-mount":
				p.parentID.file.mount++ // identity comparison; no mount probe
			case "boot", "mount-namespace", "network-namespace":
				changed := stamp
				if change == "boot" {
					changed.boot = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
				}
				if change == "mount-namespace" {
					changed.mount.Inode++
				}
				if change == "network-namespace" {
					changed.network.Inode++
				}
				observe = func(context.Context) (publicSSHKernelStamp, error) { return changed, nil }
			case "kernel-error":
				observe = func(context.Context) (publicSSHKernelStamp, error) { return publicSSHKernelStamp{}, boom }
			case "lookup-error", "mixed-enoent":
				p.lookup = func(fd int, name string) (publicSSHFileIdentity, error) {
					if name == "box" {
						if change == "mixed-enoent" {
							return publicSSHFileIdentity{}, errors.Join(unix.ENOENT, boom)
						}
						return publicSSHFileIdentity{}, boom
					}
					return publicSSHIdentity(fd, name)
				}
			case "same-boot-consumer", "other-current-boot-consumer":
				current = func(context.Context) error { return boom }
			}
			syncs := 0
			p.sync = func(int) error { syncs++; return nil }
			got, err := p.withdraw(t.Context(), current, observe)
			if err == nil || got != (PublicSSHDirectoryAbsenceResult{}) || syncs != 0 {
				t.Fatal("changed cold scope reached sync/completion", got, err, syncs)
			}
			if _, err := p.withdraw(t.Context(), publicSSHCurrentOK, func(context.Context) (publicSSHKernelStamp, error) { return stamp, nil }); err == nil || syncs != 0 {
				t.Fatal("refused cold attempt retried")
			}
		})
	}
}

func TestPublicSSHColdAbsenceFailedSyncAndPostconditionsStayConsumed(t *testing.T) {
	for _, failure := range []string{"sync", "new-child-after-sync", "current-after-sync", "boot-after-sync"} {
		t.Run(failure, func(t *testing.T) {
			root, p, observe := publicSSHColdFixture(t, publicSSHColdStamp())
			boom := errors.New("failed/lost fresh sync or current proof")
			syncs := 0
			p.sync = func(fd int) error {
				syncs++
				if failure == "sync" {
					return boom
				}
				if err := unix.Fsync(fd); err != nil {
					return err
				}
				if failure == "new-child-after-sync" {
					return os.Mkdir(filepath.Join(root, "box"), 0o700)
				}
				return nil
			}
			kernel := func(ctx context.Context) (publicSSHKernelStamp, error) {
				stamp, err := observe(ctx)
				if syncs != 0 && failure == "boot-after-sync" {
					stamp.boot = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
				}
				return stamp, err
			}
			current := func(context.Context) error {
				if syncs != 0 && failure == "current-after-sync" {
					return boom
				}
				return nil
			}
			got, err := p.withdraw(t.Context(), current, kernel)
			if err == nil || got != (PublicSSHDirectoryAbsenceResult{}) || syncs != 1 {
				t.Fatal("failed sync/postcondition became successful cold completion", got, err, syncs)
			}
			if _, err := p.withdraw(t.Context(), publicSSHCurrentOK, observe); err == nil || syncs != 1 {
				t.Fatal("failed cold sync retried")
			}
		})
	}
}

func TestPublicSSHColdAbsenceComposesOriginalACKAndFailureWithholding(t *testing.T) {
	for _, scenario := range []string{"success", "stop-failed", "rm-failed", "sync-failed"} {
		t.Run(scenario, func(t *testing.T) {
			_, p, observe := publicSSHColdFixture(t, publicSSHColdStamp())
			scope := nativeRemovalScopeFixture(t)
			boom := errors.New("scripted failed original result")
			noop := func(context.Context) error { return nil }
			a := nativeContainerRemovalActions{qualify: noop, inactive: noop, remaining: noop}
			a.stop = func(context.Context) error {
				if scenario == "stop-failed" {
					return boom
				}
				return nil
			}
			a.remove = func(context.Context) error {
				if scenario == "rm-failed" {
					return boom
				}
				return nil
			}
			a.refresh = func(_ context.Context, ack *nativeContainerStopACK) error {
				return ack.covers(scope.records[0], scope.cid)
			}
			a.retire = a.refresh
			a.namespaceMetadata = func(_ context.Context, ack *nativeContainerRemoveACK) error {
				return ack.permitsMetadata(scope.records[0], scope.cid, false)
			}
			if scenario == "sync-failed" {
				p.sync = func(int) error { return boom }
			}
			completed := false
			a.runtimeMetadata = func(ctx context.Context, ack *nativeContainerRemoveACK) error {
				_, err := p.withdraw(ctx, func(context.Context) error { return ack.permitsMetadata(scope.records[0], scope.cid, true) }, observe)
				completed = err == nil
				return err
			}
			notices := captureNativeRemovalNotices(t)
			err := completeNativeContainerRemoval(t.Context(), scope, a)
			want := scenario == "success"
			if completed != want || !want && !decision3Outcome(err, *notices, boom, scenario == "sync-failed") {
				t.Fatal("original cold protocol lost uncertainty/completion", err, completed)
			}
			if (scenario == "stop-failed" || scenario == "rm-failed") && p.attempted {
				t.Fatal("failed engine ACK reached cold suffix")
			}
		})
	}
}

func TestPublicSSHColdAbsenceInitialInactivityAndLifetimeBoundaries(t *testing.T) {
	root, p, observe := publicSSHColdFixture(t, publicSSHColdStamp())
	openCalls := 0
	open := func() (*os.File, error) { openCalls++; return nil, errors.New("unexpected lookup") }
	if got, err := snapshotPublicSSHDirectoryAbsence(t.Context(), open, "box", os.Geteuid(), os.Getegid(), publicSSHCurrentOK, func(context.Context) error { return unix.EBUSY }, observe); err == nil || got != nil || openCalls != 0 {
		t.Fatal("initial running/ambiguous task reached cold lookup")
	}
	if got, err := PreparePublicSSHDirectoryAbsence(t.Context(), "box", publicSSHCurrentOK, publicSSHCurrentOK); err == nil || got != nil {
		t.Fatal("ordinary executor constructed cold native snapshot")
	}
	if _, err := p.Withdraw(t.Context(), publicSSHCurrentOK); err == nil || p.attempted {
		t.Fatal("ordinary executor reached cold native effect")
	}
	for _, child := range []string{"box", "../box", "box/child"} {
		if child == "box" {
			if err := os.Mkdir(filepath.Join(root, child), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		if got, err := snapshotPublicSSHDirectoryAbsence(t.Context(), p.open, child, os.Geteuid(), os.Getegid(), publicSSHCurrentOK, publicSSHCurrentOK, observe); err == nil || got != nil {
			t.Fatal("present child/invalid component selected cold branch")
		}
	}
	if fmt.Sprintf("%v %#v", p, p) != "<original public SSH directory absence> <original public SSH directory absence>" {
		t.Fatal("formatter exposed cold observation")
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.withdraw(t.Context(), publicSSHCurrentOK, observe); err == nil {
		t.Fatal("closed cold observation reused")
	}
	var empty *PreparedPublicSSHDirectoryAbsence
	if _, err := empty.withdraw(t.Context(), publicSSHCurrentOK, observe); err == nil {
		t.Fatal("nil cold observation reused")
	}
}
