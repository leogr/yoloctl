package openshell

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestNativeContainerReferencesCoverCompleteOriginalPrivateTreeWithoutSecretBodies(t *testing.T) {
	f, g, inventory, _ := containerRefreshFixture(t)
	refs, err := inventory.ObserveFilesystemReferences(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := refs.MatchesSelection(g.current, f.root.Name()); err != nil {
		t.Fatal(err)
	}
	seen := 0
	err = filepath.WalkDir(f.root.Name(), func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		var st unix.Stat_t
		if err := unix.Lstat(name, &st); err != nil {
			return err
		}
		matched, err := refs.ReferencesFile(st.Dev, st.Ino)
		if err != nil || !matched {
			t.Fatal("complete reference observation omitted original member", name, err)
		}
		seen++
		return nil
	})
	if err != nil || seen != len(refs.files) {
		t.Fatal("reference observation changed complete member coverage", seen, len(refs.files), err)
	}
	// This file is outside the original tree. False is observation only, not
	// safe-cleanup authority; all actual source/ancestor checks remain separate.
	outside := filepath.Join(t.TempDir(), "unrelated")
	if err := os.WriteFile(outside, []byte("unrelated"), 0600); err != nil {
		t.Fatal(err)
	}
	var st unix.Stat_t
	if err := unix.Lstat(outside, &st); err != nil {
		t.Fatal(err)
	}
	if matched, err := refs.ReferencesFile(st.Dev, st.Ino); err != nil || matched {
		t.Fatal("reference observation claimed unrelated inode", err)
	}
	if err := refs.CheckCurrent(t.Context()); err != nil {
		t.Fatal(err)
	}
	if text := fmt.Sprintf("%v %#v", refs, refs); strings.Contains(text, "test-secret") || strings.Contains(text, "PRIVATE KEY") {
		t.Fatal("reference formatting exposed private material")
	}
}

func TestNativeContainerReferencesDetectAliasWithoutAdoptingChangedLinkOwnership(t *testing.T) {
	f, _, inventory, _ := containerRefreshFixture(t)
	refs, err := inventory.ObserveFilesystemReferences(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(f.root.Name(), "generations", inventory.current.Generation, "boundary", "server.key")
	alias := filepath.Join(t.TempDir(), "different-source-name")
	if err := os.Link(private, alias); err != nil {
		t.Fatal(err)
	}
	var st unix.Stat_t
	if err := unix.Lstat(alias, &st); err != nil {
		t.Fatal(err)
	}
	if matched, err := refs.ReferencesFile(st.Dev, st.Ino); err != nil || !matched {
		t.Fatal("same original inode hidden by another source path", err)
	}
	// No actual bind mount is created or claimed here. A hardlink changes the
	// live link count and must refuse before removal, never refresh ownership.
	if err := refs.CheckCurrent(t.Context()); err == nil {
		t.Fatal("changed private link count was adopted as current ownership")
	}
}

func TestNativeContainerReferencesRejectSubsetChangedSelectionAndClosedViews(t *testing.T) {
	for _, change := range []string{"subset-tree", "changed-record", "wrong-root", "wrong-selected", "closed", "nil", "zero-identity", "cancelled"} {
		t.Run(change, func(t *testing.T) {
			f, g, inventory, _ := containerRefreshFixture(t)
			if change == "subset-tree" {
				if len(inventory.tree.children) == 0 {
					t.Fatal("empty fixture")
				}
				original := inventory.tree.children
				inventory.tree.children = original[:len(original)-1]
				defer func() { inventory.tree.children = original }()
				if refs, err := inventory.ObserveFilesystemReferences(t.Context()); err == nil || refs != nil {
					t.Fatal("subset inventory became complete source references")
				}
				return
			}
			refs, err := inventory.ObserveFilesystemReferences(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "changed-record":
				path := filepath.Join(f.root.Name(), "generations", inventory.current.Generation, "record.json")
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				raw[0] = '['
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
				if err := refs.CheckCurrent(t.Context()); err == nil {
					t.Fatal("changed original record passed reference pre-rm check")
				}
			case "wrong-root":
				if err := refs.MatchesSelection(g.current, f.root.Name()+"-other"); err == nil {
					t.Fatal("foreign root matched original references")
				}
			case "wrong-selected":
				selected := cloneRuntimeRecord(g.current)
				selected.Launch.Binding.Box = "foreign-box"
				if err := refs.MatchesSelection(selected, f.root.Name()); err == nil {
					t.Fatal("foreign selection matched original references")
				}
			case "closed":
				if err := inventory.Close(); err != nil {
					t.Fatal(err)
				}
				if matched, err := refs.ReferencesFile(refs.rootID.device, refs.rootID.inode); err == nil || matched {
					t.Fatal("closed references yielded false-safe/positive lookup")
				}
				if refs.CheckCurrent(t.Context()) == nil || refs.MatchesSelection(g.current, f.root.Name()) == nil {
					t.Fatal("closed references remained usable")
				}
			case "nil":
				var absent *NativeContainerReferences
				if matched, err := absent.ReferencesFile(1, 1); err == nil || matched {
					t.Fatal("nil references became safe lookup")
				}
			case "zero-identity":
				if matched, err := refs.ReferencesFile(0, 0); err == nil || matched {
					t.Fatal("unknown source identity became safe absence")
				}
			case "cancelled":
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				if !errors.Is(refs.CheckCurrent(ctx), context.Canceled) {
					t.Fatal("cancelled pre-rm reference check became proof")
				}
				if observed, err := inventory.ObserveFilesystemReferences(ctx); !errors.Is(err, context.Canceled) || observed != nil {
					t.Fatal("cancelled reference capture became proof", err)
				}
			}
		})
	}
}
