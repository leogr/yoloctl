package openshell

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeRemovalReadScopeRequiresAdmittedRootBeforeCallbacks(t *testing.T) {
	f, _ := generationFixture(t)
	calls := 0
	err := withNativeRemovalReadScope(t.Context(), f.record, f.release, strings.Repeat("a", 64), func(context.Context) error { calls++; return nil }, func(*nativeRemovalReadScope) error { calls++; return nil })
	if err == nil || calls != 0 {
		t.Fatal("ordinary executor reached native removal ownership callbacks", err, calls)
	}
	scope := &nativeRemovalReadScope{}
	if err := scope.check(t.Context()); err == nil {
		t.Fatal("empty read scope became native effect authority")
	}
	if err := scope.close(); err != nil {
		t.Fatal(err)
	}
	if err := scope.check(t.Context()); err == nil {
		t.Fatal("closed native read scope remained valid")
	}
}

func TestNativeRemovalConfinedScopeRefusesAnySameIncarnationNamespaceLedger(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	control, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = control.Close() }()
	uid, gid := os.Geteuid(), os.Getegid()
	native, err := pinIdentity(int(control.Fd()), uid, gid, true)
	if err != nil {
		t.Fatal(err)
	}
	inc := strings.Repeat("a", 32)
	check := func() error { return verifyConfinedNamespaceAbsenceAt(control, native, inc, uid, gid) }
	if err := check(); err != nil {
		t.Fatal("empty namespace collection refused confined mode", err)
	}
	if err := os.Mkdir(filepath.Join(root, "namespace-pins"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "namespace-pins", strings.Repeat("b", 32)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := check(); err != nil {
		t.Fatal("another incarnation became confined cleanup scope", err)
	}
	if err := os.Mkdir(filepath.Join(root, "namespace-pins", inc), 0700); err != nil {
		t.Fatal(err)
	}
	if err := check(); err == nil {
		t.Fatal("confined scope adopted incompatible same-incarnation ledger")
	}
}

func TestNativeRemovalInfrastructureCannotReplaceAdmissionOrACKs(t *testing.T) {
	calls := 0
	verify := func(context.Context) error { calls++; return nil }
	for _, scope := range []*nativeRemovalReadScope{nil, {}, {closed: true}} {
		if scope.checkInfrastructure(t.Context(), verify) == nil || scope.checkInfrastructure(t.Context(), nil) == nil || scope.checkRuntime(t.Context()) == nil || calls != 0 {
			t.Fatal("missing native infrastructure reached current actor/storage verifier", calls)
		}
	}
}
