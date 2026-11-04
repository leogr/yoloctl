package openshell

import (
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// Pure source-owner composition fixtures. Ordinary files stand in for fields;
// no proc/namespace/current-native identity, OFD or exemption is proved here.
func remainingDescriptorOwnersFixture(t *testing.T) (*nativeRemovalReadScope, *nativeContainerRetirement, *nativeNamespaceCleanup, *nativeRuntimeCleanup, func() *os.File) {
	t.Helper()
	root := t.TempDir()
	file := func() *os.File {
		f, err := os.CreateTemp(root, "owner-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		return f
	}
	inventory := func() *NativeContainerInventory {
		return &NativeContainerInventory{root: file(), tree: &reservationNode{file: file(), identity: unix.Stat_t{Mode: unix.S_IFDIR}}}
	}
	s := &nativeRemovalReadScope{kernel: &kernelInfrastructure{proc: file(), init: file(), mount: file(), network: file()}, control: file(), containers: file(), originalReadRoot: file(), ownership: &nativeContainerOwnership{view: inventory()}}
	r := &nativeContainerRetirement{scope: s, retired: true, full: inventory()}
	n := &nativeNamespaceCleanup{retirement: r}
	c := &nativeRuntimeCleanup{namespace: n, full: r.full, parent: s.containers, steps: []runtimeCleanupStep{{parent: r.full.tree.file}}}
	return s, r, n, c, file
}

func remainingDescriptorRoles(t *testing.T, entries []originalRemainingDescriptor) map[string]*os.File {
	t.Helper()
	roles := map[string]*os.File{}
	for _, entry := range entries {
		if entry.file == nil || len(entry.roles) == 0 {
			t.Fatal("incomplete borrowed source entry")
		}
		for _, role := range entry.roles {
			if roles[role] != nil {
				t.Fatal("repeated source role", role)
			}
			roles[role] = entry.file
		}
	}
	return roles
}

func TestOriginalRemainingDescriptorsIncludeLexicalRootAndAliasRolesWithoutTransfer(t *testing.T) {
	s, r, n, c, _ := remainingDescriptorOwnersFixture(t)
	// Two source roles can borrow one actual owner pointer; distinct original
	// owner objects remain distinct even if future kernel identities match.
	r.full.root = s.ownership.view.root
	entries, err := originalRemainingDescriptors(s, r, n, c)
	if err != nil {
		t.Fatal(err)
	}
	roles := remainingDescriptorRoles(t, entries)
	for role, expected := range map[string]*os.File{
		"scope/original-read-root":          s.originalReadRoot,
		"ownership/root":                    s.ownership.view.root,
		"post-stop-full/root":               r.full.root,
		"runtime-cleanup/containers-parent": s.containers,
		"runtime-cleanup/step-parent/0":     r.full.tree.file,
	} {
		if roles[role] != expected {
			t.Fatal("original source/alias omitted", role)
		}
	}
	count := 0
	for _, entry := range entries {
		if entry.file == r.full.root {
			count++
		}
	}
	if count != 1 {
		t.Fatal("same borrowed owner was duplicated", count)
	}
	// The getter does not close/stats/dups or reconstruct descriptor ownership.
	if _, err := s.originalReadRoot.Stat(); err != nil {
		t.Fatal("getter closed lexical owner", err)
	}
	entries[0].roles[0] = "caller edit"
	again, err := originalRemainingDescriptors(s, r, n, c)
	if err != nil || remainingDescriptorRoles(t, again)["kernel/proc"] != s.kernel.proc {
		t.Fatal("returned slice mutated original owner sources", err)
	}
}

func TestOriginalRemainingDescriptorsRejectMissingForeignAndUnboundedOwners(t *testing.T) {
	for _, change := range []string{"closed", "lexical-root", "foreign-retirement", "foreign-cleanup", "failed-cleanup", "foreign-step", "cycle", "confined-pin-owner", "lost-directory", "unexpected-leaf-fd"} {
		t.Run(change, func(t *testing.T) {
			s, r, n, c, file := remainingDescriptorOwnersFixture(t)
			switch change {
			case "closed":
				s.closed = true
			case "lexical-root":
				s.originalReadRoot = nil
			case "foreign-retirement":
				r.scope = &nativeRemovalReadScope{}
			case "foreign-cleanup":
				c.full = &NativeContainerInventory{}
			case "failed-cleanup":
				c.failure = os.ErrInvalid
			case "foreign-step":
				c.steps[0].parent = file()
			case "cycle":
				s.ownership.view.tree.children = []*reservationNode{s.ownership.view.tree}
			case "confined-pin-owner":
				s.lock = &namespacePinLock{}
			case "lost-directory":
				s.ownership.view.tree.children = []*reservationNode{{name: "lost", identity: unix.Stat_t{Mode: unix.S_IFDIR}}}
			case "unexpected-leaf-fd":
				s.ownership.view.tree.children = []*reservationNode{{name: "leaf", file: file(), identity: unix.Stat_t{Mode: unix.S_IFREG}}}
			}
			if _, err := originalRemainingDescriptors(s, r, n, c); err == nil {
				t.Fatal("incomplete/foreign source owners accepted", change)
			}
		})
	}
}

func unconfinedRemainingDescriptorFixture(t *testing.T) (*nativeRemovalReadScope, *nativeContainerRetirement, *nativeNamespaceCleanup, *nativeRuntimeCleanup, *namespacePinFiles, func() *os.File) {
	t.Helper()
	s, r, n, c, file := remainingDescriptorOwnersFixture(t)
	s.selected.Launch.UnconfinedNetwork = true
	s.lock = &namespacePinLock{file: file()}
	for range 5 {
		s.lock.directories = append(s.lock.directories, pinDirectory{file: file()})
	}
	pin := &namespacePinFiles{lock: s.lock, directory: file(), record: RuntimeRecord{Generation: strings.Repeat("a", 32)}}
	s.namespaces = &namespaceGenerationInventory{lock: s.lock, pins: []*namespacePinFiles{pin}}
	n.lock = s.lock
	return s, r, n, c, pin, file
}

func TestOriginalRemainingDescriptorsIncludeSeparateStaleOwnerAndRefuseLiveRetiredNamespace(t *testing.T) {
	s, r, n, c, pin, file := unconfinedRemainingDescriptorFixture(t)
	stale := &namespacePinFiles{lock: s.lock, directory: file()}
	r.entries = []*nativeContainerRetirementEntry{{pin: pin, progress: pinRetirementProgress{completed: true}, action: &nativePreparedObservationRetirement{consumed: true, originalStaleFiles: stale}}}
	entries, err := originalRemainingDescriptors(s, r, n, c)
	if err != nil {
		t.Fatal(err)
	}
	roles := remainingDescriptorRoles(t, entries)
	if roles["retirement/stale-original/0"] != stale.directory || stale.directory == pin.directory {
		t.Fatal("distinct lexical stale owner omitted/adopted")
	}
	actualNamespace := file()
	r.entries[0].sameBoot = true
	r.entries[0].action = &nativePreparedPinRetirement{consumed: true, remainingNamespace: func() *os.File { return actualNamespace }}
	if _, err := originalRemainingDescriptors(s, r, n, c); err == nil {
		t.Fatal("same-boot live namespace became an exemption")
	}
	r.entries[0].action = &nativePreparedPinRetirement{consumed: true, remainingNamespace: func() *os.File { return nil }}
	if _, err := originalRemainingDescriptors(s, r, n, c); err != nil {
		t.Fatal("exact source-level closed branch refused", err)
	}
}

func TestRemainingDescriptorAliasesNeverAcquireAnotherCloseOwner(t *testing.T) {
	s, _, _, _, file := remainingDescriptorOwnersFixture(t)
	borrowed := s.originalReadRoot
	if err := s.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := borrowed.Stat(); err != nil {
		t.Fatal("scope alias double-closed lexical owner", err)
	}
	stale := &namespacePinFiles{directory: file()}
	calls := 0
	action := &nativePreparedObservationRetirement{originalStaleFiles: stale, release: func() error { calls++; return nil }}
	for range 2 {
		if err := action.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 || action.originalStaleFiles != nil {
		t.Fatal("stale borrowed source escaped original Close", calls)
	}
	pin := &nativePreparedPinRetirement{remainingNamespace: func() *os.File { return nil }, release: func() error { calls++; return nil }}
	for range 2 {
		if err := pin.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 || pin.remainingNamespace != nil {
		t.Fatal("namespace accessor acquired a second release or survived Close", calls)
	}
}
