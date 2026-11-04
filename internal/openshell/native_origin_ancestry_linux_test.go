package openshell

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"golang.org/x/sys/unix"
)

// Ordinary private-directory fixtures model
// mapping/lifetime behavior; nil-error callbacks prove NO native UUID/VM grant.
func nativeOriginAncestryRootFixture(t *testing.T, count int, rootMode os.FileMode) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, rootMode); err != nil {
		t.Fatal(err)
	}
	directory := root
	for index := 1; index <= count; index++ {
		name, _ := nativeOriginAncestryComponent(index)
		directory = filepath.Join(directory, name)
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
func nativeOriginAncestryCurrentFixture(context.Context) error { return nil }

func TestNativeOriginAncestryDistinguishesEachExactPrefixAndExpires(t *testing.T) {
	states := []nativeOriginAncestryState{nativeOriginAncestryMissingRuntime, nativeOriginAncestryMissingControl, nativeOriginAncestryMissingContainers, nativeOriginAncestryPresent}
	names := []string{"runtime", "openshell", "containers", ""}
	for count := 0; count <= 3; count++ {
		t.Run(names[count]+"-prefix", func(t *testing.T) {
			root := nativeOriginAncestryRootFixture(t, count, 0755)
			var escaped *nativeOriginAncestry
			var owned []*os.File
			err := withNativeOriginAncestryAt(t.Context(), root, os.Geteuid(), os.Getegid(), nativeOriginAncestryCurrentFixture, func(p *nativeOriginAncestry) error {
				escaped = p
				for _, node := range p.nodes {
					owned = append(owned, node.file)
				}
				if err := p.Check(t.Context()); err != nil {
					return err
				}
				view, err := p.View()
				if err != nil {
					return err
				}
				if view.state != states[count] || view.missingComponent != names[count] || len(view.presentPrefix) != count || view.namespacePinsObserved {
					t.Fatal("wrong fixed-prefix/sibling observation")
				}
				if view.disk.device == 0 || view.disk.mount == 0 || view.disk.inode == 0 {
					t.Fatal("unmapped disk observation")
				}
				if (view.control.inode != 0) != (count >= 2) || (view.containers.inode != 0) != (count == 3) {
					t.Fatal("invented absent-prefix identity")
				}
				if count > 0 {
					view.presentPrefix[0] = "changed-copy"
					again, _ := p.View()
					if again.presentPrefix[0] != "runtime" {
						t.Fatal("caller modified original fixed scope")
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, file := range owned {
				if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
					t.Fatal("original ancestry handle leaked", err)
				}
			}
			if _, err := escaped.View(); err == nil {
				t.Fatal("escaped callback retained valid view")
			}
			if err := escaped.Check(t.Context()); err == nil {
				t.Fatal("escaped callback repaired scope")
			}
		})
	}
}

func TestNativeOriginAncestryRespectsRootAndPrivateDescendantModes(t *testing.T) {
	for _, mode := range []os.FileMode{0700, 0750, 0755} {
		root := nativeOriginAncestryRootFixture(t, 3, mode)
		if err := withNativeOriginAncestryAt(t.Context(), root, os.Geteuid(), os.Getegid(), nativeOriginAncestryCurrentFixture, func(*nativeOriginAncestry) error { return nil }); err != nil {
			t.Fatal("protected mounted-disk root refused", mode, err)
		}
	}
	for _, which := range []string{"writable-root", "special-root", "permissive-runtime", "foreign-owner", "regular-runtime", "symlink-runtime"} {
		t.Run(which, func(t *testing.T) {
			root := nativeOriginAncestryRootFixture(t, 0, 0755)
			uid, gid := os.Geteuid(), os.Getegid()
			switch which {
			case "writable-root":
				if err := os.Chmod(root, 0777); err != nil {
					t.Fatal(err)
				}
			case "special-root":
				if err := os.Chmod(root, 0755|os.ModeSticky); err != nil {
					t.Fatal(err)
				}
			case "permissive-runtime":
				if err := os.Mkdir(filepath.Join(root, "runtime"), 0755); err != nil {
					t.Fatal(err)
				}
			case "foreign-owner":
				uid++
			case "regular-runtime":
				if err := os.WriteFile(filepath.Join(root, "runtime"), []byte("not a directory"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink-runtime":
				if err := os.Symlink(t.TempDir(), filepath.Join(root, "runtime")); err != nil {
					t.Fatal(err)
				}
			}
			called := false
			err := withNativeOriginAncestryAt(t.Context(), root, uid, gid, nativeOriginAncestryCurrentFixture, func(*nativeOriginAncestry) error { called = true; return nil })
			if err == nil || called {
				t.Fatal("unsafe/error prefix became qualified absence")
			}
		})
	}
}

func TestNativeOriginAncestryFirstAbsentChildAppearingIsSticky(t *testing.T) {
	root := nativeOriginAncestryRootFixture(t, 0, 0755)
	var escaped *nativeOriginAncestry
	err := withNativeOriginAncestryAt(t.Context(), root, os.Geteuid(), os.Getegid(), nativeOriginAncestryCurrentFixture, func(p *nativeOriginAncestry) error {
		escaped = p
		if err := os.Mkdir(filepath.Join(root, "runtime"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := p.Check(t.Context()); err == nil {
			t.Fatal("new fixed child kept absence valid")
		}
		if _, err := p.View(); err == nil {
			t.Fatal("failed scope still returned absence")
		}
		return nil
	})
	if err == nil || escaped.failure == nil {
		t.Fatal("observed uncertainty repaired at callback return")
	}
}

func TestNativeOriginAncestryRetainedPrefixReplacementRefuses(t *testing.T) {
	root := nativeOriginAncestryRootFixture(t, 1, 0755)
	err := withNativeOriginAncestryAt(t.Context(), root, os.Geteuid(), os.Getegid(), nativeOriginAncestryCurrentFixture, func(p *nativeOriginAncestry) error {
		if err := os.Rename(filepath.Join(root, "runtime"), filepath.Join(root, "old-runtime")); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(root, "runtime"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := p.Check(t.Context()); err == nil {
			t.Fatal("replacement matching-mode prefix adopted")
		}
		return nil
	})
	if err == nil {
		t.Fatal("callback finalization lost changed original prefix")
	}
}

func TestNativeOriginAncestryOriginalContextCannotBeRevived(t *testing.T) {
	root := nativeOriginAncestryRootFixture(t, 2, 0755)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	err := withNativeOriginAncestryAt(ctx, root, os.Geteuid(), os.Getegid(), nativeOriginAncestryCurrentFixture, func(p *nativeOriginAncestry) error {
		cancel()
		if err := p.Check(context.WithoutCancel(ctx)); err == nil {
			t.Fatal("WithoutCancel revived original execution scope")
		}
		if _, err := p.View(); err == nil {
			t.Fatal("expired original scope yielded copied state")
		}
		return nil
	})
	if err == nil {
		t.Fatal("expired original callback became success")
	}
	for _, p := range []*nativeOriginAncestry{nil, {}} {
		if _, err := p.View(); err == nil {
			t.Fatal("zero/unverified ancestry became absence")
		}
	}
}

func TestNativeOriginAncestryStorageFailureAndOwnedCloseRemainOriginal(t *testing.T) {
	root := nativeOriginAncestryRootFixture(t, 0, 0755)
	failure := errors.New("actual storage unavailable")
	called := false
	err := withNativeOriginAncestryAt(t.Context(), root, os.Geteuid(), os.Getegid(), func(context.Context) error { return failure }, func(*nativeOriginAncestry) error { called = true; return nil })
	if !errors.Is(err, failure) || called {
		t.Fatal("storage failure became absence")
	}
	original := errors.New("callback failed")
	var escaped *nativeOriginAncestry
	err = withNativeOriginAncestryAt(t.Context(), root, os.Geteuid(), os.Getegid(), nativeOriginAncestryCurrentFixture, func(p *nativeOriginAncestry) error {
		escaped = p
		if err := p.nodes[0].file.Close(); err != nil {
			t.Fatal(err)
		} // fixture owns this exact handle
		return original
	})
	if !errors.Is(err, original) || escaped.closeErr == nil || !errors.Is(err, escaped.closeErr) {
		t.Fatal("original callback or close failure lost", err)
	}
	if repeated := escaped.Close(); !reflect.DeepEqual(repeated, escaped.closeErr) {
		t.Fatal("Close failure retried/replaced")
	}
}

func TestNativeOriginAncestryFinalVerifierCannotHideCancellation(t *testing.T) {
	for _, phase := range []string{"capture", "callback-check"} {
		t.Run(phase, func(t *testing.T) {
			root := nativeOriginAncestryRootFixture(t, 0, 0755)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls, cancelAt := 0, 3
			if phase == "callback-check" {
				cancelAt = 5
			}
			verify := func(context.Context) error {
				calls++
				if calls == cancelAt {
					cancel()
				}
				return nil
			}
			used := false
			err := withNativeOriginAncestryAt(ctx, root, os.Geteuid(), os.Getegid(), verify, func(p *nativeOriginAncestry) error {
				used = true
				if err := p.Check(ctx); !errors.Is(err, context.Canceled) {
					t.Fatal("final callback cancellation lost", err)
				}
				if err := p.Check(t.Context()); err == nil {
					t.Fatal("fresh context repaired failed scope")
				}
				if _, err := p.View(); err == nil {
					t.Fatal("cancelled scope yielded absence")
				}
				return nil
			})
			if !errors.Is(err, context.Canceled) || used != (phase == "callback-check") || calls != cancelAt {
				t.Fatal("final cancellation became usable", err, used, calls, cancelAt)
			}
		})
	}
}

func TestNativeOriginAncestryMissingChildAppearingInVerifierRefuses(t *testing.T) {
	root := nativeOriginAncestryRootFixture(t, 0, 0755)
	calls := 0
	used := false
	err := withNativeOriginAncestryAt(t.Context(), root, os.Geteuid(), os.Getegid(), func(context.Context) error {
		calls++
		if calls == 3 {
			if err := os.Mkdir(filepath.Join(root, "runtime"), 0700); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	}, func(*nativeOriginAncestry) error { used = true; return nil })
	if err == nil || used || calls != 3 {
		t.Fatal("new child during verification became absence", err, used, calls)
	}
}

func TestNativeOriginAncestryCloseOnExecLossRefuses(t *testing.T) {
	root := nativeOriginAncestryRootFixture(t, 0, 0755)
	err := withNativeOriginAncestryAt(t.Context(), root, os.Geteuid(), os.Getegid(), nativeOriginAncestryCurrentFixture, func(p *nativeOriginAncestry) error {
		if _, err := unix.FcntlInt(p.nodes[0].file.Fd(), unix.F_SETFD, 0); err != nil {
			t.Fatal(err)
		}
		if err := p.Check(t.Context()); err == nil {
			t.Fatal("non-CLOEXEC original scope accepted")
		}
		return nil
	})
	if err == nil {
		t.Fatal("lost confinement/lifetime predicate became success")
	}
}
