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

func publicSSHWithdrawalFixture(t *testing.T, present bool) (string, *runtimeSSH, *PreparedPublicSSHWithdrawal) {
	t.Helper()
	dir := runtimeSocketFixture(t)
	root := dir.Name()
	var endpoint *runtimeSSH
	var err error
	if present {
		endpoint, err = listenRuntimeSSHAt(dir, os.Geteuid(), os.Getegid())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = endpoint.Close() })
	}
	p, err := snapshotPublicSSHWithdrawal(t.Context(), func() (*os.File, error) {
		fd, err := unix.Openat2(unix.AT_FDCWD, root, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS})
		if err != nil {
			return nil, err
		}
		return os.NewFile(uintptr(fd), root), nil
	}, os.Geteuid(), os.Getegid(), publicSSHCurrentOK)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return root, endpoint, p
}

func publicSSHCurrentOK(context.Context) error { return nil }

func TestPublicSSHWithdrawalSeparatesOriginalUnlinkAndFreshAbsence(t *testing.T) {
	for _, scenario := range []string{"present", "helper-close", "originally-absent"} {
		t.Run(scenario, func(t *testing.T) {
			root, endpoint, p := publicSSHWithdrawalFixture(t, scenario != "originally-absent")
			if scenario == "helper-close" {
				if err := endpoint.Close(); err != nil {
					t.Fatal("actual helper Close failed", err)
				}
			}
			var effects []string
			p.unlink = func(fd int, name string, flags int) error {
				effects = append(effects, "unlink")
				return unix.Unlinkat(fd, name, flags)
			}
			p.sync = func(fd int) error { effects = append(effects, "sync"); return unix.Fsync(fd) }
			result, err := p.withdraw(t.Context(), publicSSHCurrentOK)
			absent := scenario != "present"
			if err != nil || result != (PublicSSHWithdrawalResult{ObservedAbsent: absent, UnlinkAcknowledged: !absent, DirectorySynced: true}) {
				t.Fatal("absence substituted for unlink ACK or fresh sync lost", result, err)
			}
			want := "[unlink sync]"
			if absent {
				want = "[sync]"
			}
			if fmt.Sprint(effects) != want {
				t.Fatal("wrong original effect sequence", effects)
			}
			if _, err := os.Lstat(filepath.Join(root, "ssh.sock")); !os.IsNotExist(err) {
				t.Fatal("endpoint postcondition false", err)
			}
			before := len(effects)
			if _, err := p.withdraw(t.Context(), publicSSHCurrentOK); err == nil || len(effects) != before {
				t.Fatal("completed withdrawal replayed")
			}
		})
	}
}

func TestPublicSSHWithdrawalRejectsReplacementAndMappingDrift(t *testing.T) {
	for _, change := range []string{"socket", "new-after-original-absence", "symlink", "directory", "missing-directory", "directory-mode", "socket-mode", "mount-id"} {
		t.Run(change, func(t *testing.T) {
			root, endpoint, p := publicSSHWithdrawalFixture(t, change != "new-after-original-absence")
			switch change {
			case "socket", "new-after-original-absence", "symlink":
				if endpoint != nil {
					if err := endpoint.Close(); err != nil {
						t.Fatal(err)
					}
				}
				if change == "symlink" {
					if err := os.Symlink("missing", filepath.Join(root, "ssh.sock")); err != nil {
						t.Fatal(err)
					}
				} else {
					replacement, err := listenRuntimeSSHAt(p.directory, os.Geteuid(), os.Getegid())
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = replacement.Close() })
				}
			case "directory", "missing-directory":
				if err := os.Rename(root, root+"-old"); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Rename(root+"-old", root) })
				if change == "directory" {
					if err := os.Mkdir(root, 0o700); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = os.Remove(root) })
				}
			case "directory-mode":
				if err := os.Chmod(root, 0o777); err != nil {
					t.Fatal(err)
				}
			case "socket-mode":
				if err := os.Chmod(filepath.Join(root, "ssh.sock"), 0o666); err != nil {
					t.Fatal(err)
				}
			case "mount-id":
				// Comparison regression only; no mount/syscall probe is performed.
				p.dirID.file.mount++
			}
			effects := 0
			p.unlink = func(int, string, int) error { effects++; return nil }
			p.sync = func(int) error { effects++; return nil }
			if result, err := p.withdraw(t.Context(), publicSSHCurrentOK); err == nil || result != (PublicSSHWithdrawalResult{}) || effects != 0 {
				t.Fatal("changed original reached effects", result, err, effects)
			}
		})
	}
}

func TestPublicSSHWithdrawalFailedEffectsAndUnknownReadsStaySticky(t *testing.T) {
	for _, failure := range []string{"current", "lookup", "mixed-enoent", "unlink", "unlink-enoent", "sync-present", "sync-absent", "post-lookup", "post-mapping", "post-socket"} {
		t.Run(failure, func(t *testing.T) {
			root, endpoint, p := publicSSHWithdrawalFixture(t, true)
			if failure == "sync-absent" {
				if err := endpoint.Close(); err != nil {
					t.Fatal(err)
				}
			}
			boom := errors.New("actual failed/lost operation")
			calls, leaves, checks := 0, 0, 0
			p.lookup = func(fd int, name string) (publicSSHFileIdentity, error) {
				calls++
				if name == "ssh.sock" {
					leaves++
					if failure == "lookup" || failure == "post-lookup" && leaves == 2 {
						return publicSSHFileIdentity{}, boom
					}
					if failure == "mixed-enoent" {
						return publicSSHFileIdentity{}, errors.Join(unix.ENOENT, boom)
					}
				}
				return publicSSHIdentity(fd, name)
			}
			p.unlink = func(fd int, name string, flags int) error {
				calls++
				if failure == "unlink" {
					return boom
				}
				if failure == "unlink-enoent" {
					// Visible effect with a failed/lost reply: later absence must
					// not substitute for this invocation's acknowledgement.
					if err := unix.Unlinkat(fd, name, flags); err != nil {
						return err
					}
					return unix.ENOENT
				}
				return unix.Unlinkat(fd, name, flags)
			}
			p.sync = func(fd int) error {
				calls++
				if failure == "sync-present" || failure == "sync-absent" {
					return boom
				}
				return unix.Fsync(fd)
			}
			current := func(context.Context) error {
				checks++
				if failure == "current" {
					return boom
				}
				if checks == 2 && failure == "post-mapping" {
					return boom // failed fresh mapping/storage proof after sync
				}
				if checks == 2 && failure == "post-socket" {
					if err := os.WriteFile(filepath.Join(root, "ssh.sock"), nil, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				return nil
			}
			result, err := p.withdraw(t.Context(), current)
			if err == nil || result != (PublicSSHWithdrawalResult{}) || failure != "post-socket" && failure != "unlink-enoent" && !errors.Is(err, boom) {
				t.Fatal("failed result became successful endpoint completion", result, err)
			}
			before, priorChecks := calls, checks
			if _, err := p.withdraw(t.Context(), publicSSHCurrentOK); err == nil || calls != before || checks != priorChecks {
				t.Fatal("failed effect/absence observation retried", err)
			}
		})
	}
}

// The real original-call protocol and helper Close are composed here; engine,
// namespace and current-consumer proofs are scripted, not native qualification.
func TestPublicSSHWithdrawalComposesOriginalRemovalACKAndHelperClose(t *testing.T) {
	for _, scenario := range []string{"present", "helper-close", "lost-stop", "lost-rm", "failed-sync"} {
		t.Run(scenario, func(t *testing.T) {
			_, endpoint, p := publicSSHWithdrawalFixture(t, true)
			scope := nativeRemovalScopeFixture(t)
			boom := errors.New("scripted failed/lost reply")
			if scenario == "failed-sync" {
				p.sync = func(int) error { return boom }
			}
			noop := func(context.Context) error { return nil }
			a := nativeContainerRemovalActions{qualify: noop, inactive: noop, remaining: noop}
			a.stop = func(context.Context) error {
				if scenario == "helper-close" || scenario == "lost-stop" {
					if err := endpoint.Close(); err != nil {
						return err
					}
				}
				if scenario == "lost-stop" {
					return boom
				}
				return nil
			}
			a.remove = func(context.Context) error {
				if scenario == "lost-rm" {
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
			completed := false
			a.runtimeMetadata = func(ctx context.Context, ack *nativeContainerRemoveACK) error {
				_, err := p.withdraw(ctx, func(context.Context) error { return ack.permitsMetadata(scope.records[0], scope.cid, true) })
				completed = err == nil
				return err
			}
			notices := captureNativeRemovalNotices(t)
			err := completeNativeContainerRemoval(t.Context(), scope, a)
			want := scenario == "present" || scenario == "helper-close"
			if completed != want || want && err != nil || !want && !decision3Outcome(err, *notices, boom, scenario != "lost-stop" && scenario != "lost-rm") {
				t.Fatal("original protocol lost endpoint failure/completion boundary", err, completed)
			}
			if (scenario == "lost-stop" || scenario == "lost-rm") && p.attempted {
				t.Fatal("failed engine ACK reached endpoint withdrawal")
			}
		})
	}
}

func TestPublicSSHWithdrawalExecutorAndLifetimeBoundaries(t *testing.T) {
	_, _, p := publicSSHWithdrawalFixture(t, false)
	if got, err := PreparePublicSSHWithdrawal(t.Context(), "box", publicSSHCurrentOK); err == nil || got != nil {
		t.Fatal("ordinary executor constructed native withdrawal")
	}
	if _, err := p.Withdraw(t.Context(), publicSSHCurrentOK); err == nil || p.attempted {
		t.Fatal("ordinary executor reached native effect")
	}
	opened := 0
	open := func() (*os.File, error) { opened++; return nil, errors.New("should not open") }
	for _, verify := range []func(context.Context) error{nil, func(context.Context) error { return unix.EACCES }} {
		if got, err := snapshotPublicSSHWithdrawal(t.Context(), open, os.Geteuid(), os.Getegid(), verify); err == nil || got != nil || opened != 0 {
			t.Fatal("missing/failed actual owner check reached lookup")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got, err := snapshotPublicSSHWithdrawal(ctx, open, os.Geteuid(), os.Getegid(), publicSSHCurrentOK); !errors.Is(err, context.Canceled) || got != nil || opened != 0 {
		t.Fatal("canceled observation reached lookup", err)
	}
	if fmt.Sprintf("%v %#v", p, p) != "<original public SSH withdrawal> <original public SSH withdrawal>" {
		t.Fatal("formatter exposed retained observation")
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.withdraw(t.Context(), publicSSHCurrentOK); err == nil {
		t.Fatal("closed observation reused")
	}
	var empty *PreparedPublicSSHWithdrawal
	if _, err := empty.withdraw(t.Context(), publicSSHCurrentOK); err == nil {
		t.Fatal("nil observation accepted")
	}
}
