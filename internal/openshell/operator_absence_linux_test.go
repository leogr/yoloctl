package openshell

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/operator"
	"golang.org/x/sys/unix"
)

func operatorAbsenceFixture(t *testing.T, missingRoot bool) (string, *PreparedOperatorEndpointAbsence, func(context.Context) (publicSSHKernelStamp, error)) {
	t.Helper()
	base := t.TempDir()
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	if !missingRoot {
		if err := os.Mkdir(filepath.Join(base, "operators"), 0o755); err != nil {
			t.Fatal(err)
		}
		// An unrelated retained same-box incarnation is deliberately present.
		// Selected absence must not delete or discharge that registration.
		sibling := filepath.Join(base, "operators", strings.Repeat("b", 32))
		if err := os.Mkdir(sibling, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sibling, "identity.json"), []byte(`{"box":"selected","incarnation":"`+strings.Repeat("b", 32)+`"}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	observe := func(context.Context) (publicSSHKernelStamp, error) { return publicSSHColdStamp(), nil }
	open := func() (*os.File, error) {
		fd, err := unix.Openat2(unix.AT_FDCWD, base, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS})
		if err != nil {
			return nil, err
		}
		return os.NewFile(uintptr(fd), "test protected operator parent"), nil
	}
	p, err := snapshotOperatorEndpointAbsence(t.Context(), open, operator.Endpoint{Box: "selected", Incarnation: strings.Repeat("A", 32)}, os.Geteuid(), os.Getegid(), publicSSHCurrentOK, publicSSHCurrentOK, observe)
	if err != nil {
		t.Fatal("valid original absence fixture refused", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return base, p, observe
}

func TestOperatorEndpointAbsencePreservesOriginalBranchAndSibling(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "selected-absent", true: "root-absent"}[missing], func(t *testing.T) {
			base, p, observe := operatorAbsenceFixture(t, missing)
			if !missing {
				// Independent publication may change parent bookkeeping, while
				// the named/retained original root mapping remains unchanged.
				if err := os.Mkdir(filepath.Join(base, "operators", strings.Repeat("c", 32)), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			syncs := 0
			p.sync = func(fd int) error {
				syncs++
				want := p.base
				if !missing {
					want = p.root
				}
				if fd != int(want.Fd()) {
					t.Fatal("synced another parent")
				}
				return unix.Fsync(fd)
			}
			got, err := p.withdraw(t.Context(), publicSSHCurrentOK, observe)
			want := OperatorEndpointAbsenceResult{ObservedRegistrationRootAbsent: missing, ObservedSelectedRegistrationAbsent: !missing, ParentSynced: true}
			if err != nil || got != want || syncs != 1 {
				t.Fatal("cold operator absence lost branch/fresh sync", got, err, syncs)
			}
			if missing {
				if _, err := os.Lstat(filepath.Join(base, "operators")); !os.IsNotExist(err) {
					t.Fatal("cold operator prefix was created/adopted", err)
				}
			} else if raw, err := os.ReadFile(filepath.Join(base, "operators", strings.Repeat("b", 32), "identity.json")); err != nil || !strings.Contains(string(raw), `"box":"selected"`) {
				t.Fatal("selected absence removed another same-box registration", err)
			}
			if _, err := p.withdraw(t.Context(), publicSSHCurrentOK, observe); err == nil || syncs != 1 {
				t.Fatal("completed absence was replayed")
			}
		})
	}
}

func TestOperatorEndpointAbsenceRejectsChangedOriginalScopeBeforeSync(t *testing.T) {
	for _, missing := range []bool{false, true} {
		branch := map[bool]string{false: "selected", true: "root"}[missing]
		for _, change := range []string{"child-directory", "child-file", "child-link", "root-removed", "root-replaced", "base-replaced", "base-mode", "root-mode", "base-mount", "root-mount", "boot", "mount-namespace", "network-namespace", "kernel-error", "lookup-error", "mixed-enoent", "consumer", "listener-stream"} {
			t.Run(branch+"/"+change, func(t *testing.T) {
				base, p, observe := operatorAbsenceFixture(t, missing)
				boom := errors.New("scripted failed current proof")
				current := publicSSHCurrentOK
				child := filepath.Join(base, "operators")
				if !missing {
					child = filepath.Join(child, p.endpoint.Incarnation)
				}
				switch change {
				case "child-directory":
					if err := os.Mkdir(child, 0o755); err != nil {
						t.Fatal(err)
					}
				case "child-file":
					if err := os.WriteFile(child, nil, 0o600); err != nil {
						t.Fatal(err)
					}
				case "child-link":
					if err := os.Symlink("missing", child); err != nil {
						t.Fatal(err)
					}
				case "root-removed", "root-replaced":
					if !missing {
						root := filepath.Join(base, "operators")
						if err := os.Rename(root, root+"-retained"); err != nil {
							t.Fatal(err)
						}
						if change == "root-replaced" {
							if err := os.Mkdir(root, 0o755); err != nil {
								t.Fatal(err)
							}
						}
					} else {
						if err := os.Mkdir(child, 0o755); err != nil {
							t.Fatal(err)
						}
					}
				case "base-replaced":
					if err := os.Rename(base, base+"-retained"); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = os.Rename(base+"-retained", base) })
					if err := os.Mkdir(base, 0o700); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = os.Remove(base) })
				case "base-mode":
					if err := os.Chmod(base, 0o777); err != nil {
						t.Fatal(err)
					}
				case "root-mode":
					name := base
					if !missing {
						name = filepath.Join(base, "operators")
					}
					if err := os.Chmod(name, 0o777); err != nil {
						t.Fatal(err)
					}
				case "base-mount":
					p.baseID.file.mount++
				case "root-mount":
					if missing {
						p.baseID.file.mount++
					} else {
						p.rootID.file.mount++
					}
				case "boot", "mount-namespace", "network-namespace", "kernel-error":
					observe = func(context.Context) (publicSSHKernelStamp, error) {
						stamp := publicSSHColdStamp()
						if change == "boot" {
							stamp.boot = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
						}
						if change == "mount-namespace" {
							stamp.mount.Inode++
						}
						if change == "network-namespace" {
							stamp.network.Inode++
						}
						if change == "kernel-error" {
							return publicSSHKernelStamp{}, boom
						}
						return stamp, nil
					}
				case "lookup-error", "mixed-enoent":
					p.lookup = func(fd int, name string) (publicSSHFileIdentity, error) {
						if name == "operators" && missing || name == p.endpoint.Incarnation && !missing {
							if change == "mixed-enoent" {
								return publicSSHFileIdentity{}, errors.Join(unix.ENOENT, boom)
							}
							return publicSSHFileIdentity{}, boom
						}
						return publicSSHIdentity(fd, name)
					}
				case "consumer", "listener-stream":
					current = func(context.Context) error { return boom }
				}
				syncs := 0
				p.sync = func(int) error { syncs++; return nil }
				got, err := p.withdraw(t.Context(), current, observe)
				if err == nil || got != (OperatorEndpointAbsenceResult{}) || syncs != 0 {
					t.Fatal("changed operator scope reached sync/completion", got, err, syncs)
				}
				if _, err := p.withdraw(t.Context(), publicSSHCurrentOK, func(context.Context) (publicSSHKernelStamp, error) { return publicSSHColdStamp(), nil }); err == nil || syncs != 0 {
					t.Fatal("failed operator absence retried")
				}
			})
		}
	}
}

func TestOperatorEndpointAbsenceFailedSyncOrPostconditionRemainsConsumed(t *testing.T) {
	for _, missing := range []bool{false, true} {
		for _, failure := range []string{"sync", "child-after-sync", "current-after-sync"} {
			t.Run(map[bool]string{false: "selected", true: "root"}[missing]+"/"+failure, func(t *testing.T) {
				base, p, observe := operatorAbsenceFixture(t, missing)
				syncs := 0
				boom := errors.New("scripted failed/lost parent sync or current proof")
				p.sync = func(fd int) error {
					syncs++
					if failure == "sync" {
						return boom
					}
					if err := unix.Fsync(fd); err != nil {
						return err
					}
					if failure == "child-after-sync" {
						child := filepath.Join(base, "operators")
						if !missing {
							child = filepath.Join(child, p.endpoint.Incarnation)
						}
						return os.Mkdir(child, 0o755)
					}
					return nil
				}
				current := func(context.Context) error {
					if syncs != 0 && failure == "current-after-sync" {
						return boom
					}
					return nil
				}
				got, err := p.withdraw(t.Context(), current, observe)
				if err == nil || got != (OperatorEndpointAbsenceResult{}) || syncs != 1 {
					t.Fatal("failed operator sync/postcondition completed", got, err, syncs)
				}
				if _, err := p.withdraw(t.Context(), publicSSHCurrentOK, observe); err == nil || syncs != 1 {
					t.Fatal("failed operator sync replayed")
				}
			})
		}
	}
}

func TestOperatorEndpointAbsenceComposesOriginalACKWithholding(t *testing.T) {
	for _, missing := range []bool{false, true} {
		for _, scenario := range []string{"success", "stop-failed", "rm-failed", "sync-failed"} {
			t.Run(map[bool]string{false: "selected", true: "root"}[missing]+"/"+scenario, func(t *testing.T) {
				_, p, observe := operatorAbsenceFixture(t, missing)
				scope := nativeRemovalScopeFixture(t)
				boom := errors.New("failed original effect reply")
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
				withdrawals, settled := 0, false
				if scenario == "sync-failed" {
					p.sync = func(int) error { return boom }
				}
				a.runtimeMetadata = func(c context.Context, ack *nativeContainerRemoveACK) error {
					if err := ack.permitsMetadata(scope.records[0], scope.cid, true); err != nil {
						return err
					}
					withdrawals++
					_, err := p.withdraw(c, noop, observe)
					if err == nil {
						settled = true
					}
					return err
				}
				notices := captureNativeRemovalNotices(t)
				err := completeNativeContainerRemoval(t.Context(), scope, a)
				if scenario == "success" {
					if err != nil || !settled || withdrawals != 1 {
						t.Fatal("original ACK suffix did not complete", err)
					}
				} else {
					if !decision3Outcome(err, *notices, boom, scenario == "sync-failed") || settled {
						t.Fatal("failed original ACK became endpoint completion", err)
					}
					if scenario != "sync-failed" && withdrawals != 0 {
						t.Fatal("failed Stop/rm reached absence suffix")
					}
				}
			})
		}
	}
}

func TestOperatorEndpointAbsenceRequiresInitialOwnershipInactivityAndNativeScope(t *testing.T) {
	base, p, observe := operatorAbsenceFixture(t, false)
	open := p.openBase
	for _, which := range []string{"owner", "inactive"} {
		owned, inactive := publicSSHCurrentOK, publicSSHCurrentOK
		boom := errors.New("initial proof unavailable")
		if which == "owner" {
			owned = func(context.Context) error { return boom }
		} else {
			inactive = func(context.Context) error { return boom }
		}
		if got, err := snapshotOperatorEndpointAbsence(t.Context(), open, p.endpoint, os.Geteuid(), os.Getegid(), owned, inactive, observe); !errors.Is(err, boom) || got != nil {
			t.Fatal("initial owner/inactivity refusal bypassed", err)
		}
	}
	// Present partial registration must refuse before any absence capability.
	if err := os.Mkdir(filepath.Join(base, "operators", p.endpoint.Incarnation), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := snapshotOperatorEndpointAbsence(t.Context(), open, p.endpoint, os.Geteuid(), os.Getegid(), publicSSHCurrentOK, publicSSHCurrentOK, observe); err == nil || got != nil {
		t.Fatal("present partial registration became absence")
	}
	if got, err := PrepareOperatorEndpointAbsence(t.Context(), p.endpoint, publicSSHCurrentOK, publicSSHCurrentOK); err == nil || got != nil {
		t.Fatal("ordinary executor entered native operator infrastructure")
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.withdraw(t.Context(), publicSSHCurrentOK, observe); err == nil {
		t.Fatal("closed original observation reused")
	}
	zero := new(PreparedOperatorEndpointAbsence)
	if _, err := zero.withdraw(t.Context(), publicSSHCurrentOK, observe); err == nil {
		t.Fatal("zero observation fabricated absence")
	}
}
