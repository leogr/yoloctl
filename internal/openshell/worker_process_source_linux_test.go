//go:build linux

package openshell

import (
	"context"
	"errors"
	"os"
	"reflect"
	"runtime"
	"testing"

	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/vmanchor"
	"github.com/leogr/yoloctl/internal/workercheckpoint"
	"golang.org/x/sys/unix"
)

// NO fake NativeCtx installation, callback success backend, numeric anchor,
// fixture ops, VM gate or positive original-source factory is used here.
func TestWorkerSourceNilOrZeroHandoffCannotReachCallback(t *testing.T) {
	for _, original := range []*vmanchor.WorkerHandoff{nil, new(vmanchor.WorkerHandoff)} {
		called := false
		err := WithNativeWorkerSource(t.Context(), original, func(*NativeWorkerSource) error { called = true; return nil })
		if err == nil || called {
			t.Fatal("nil/zero handoff minted source", err, called)
		}
	}
}
func TestWorkerSourceZeroOrExpiredAttachmentCannotOpenProcessBorrow(t *testing.T) {
	for _, source := range []*NativeWorkerSource{nil, new(NativeWorkerSource)} {
		called := false
		err := source.WithProcessObservation(t.Context(), "box", func(*NativeWorkerProcessRead) error { called = true; return nil })
		if err == nil || called {
			t.Fatal("zero source reached process observer", err, called)
		}
	}
}
func TestWorkerProcessReadRejectsExpiredAndForeignThreadBeforeScopeIO(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// Deliberately INCOMPLETE negative borrow, with no token/proc/namespace.
	scope := &nativeWorkerProcessScope{pid: os.Getpid(), tid: unix.Gettid() + 1}
	read := &NativeWorkerProcessRead{scope: scope}
	if err := read.Check(t.Context()); err == nil {
		t.Fatal("inactive read accepted")
	}
	read.active.Store(true)
	if err := read.Check(t.Context()); err == nil {
		t.Fatal("foreign TID read accepted")
	}
	read.active.Store(false)
	if err := read.Check(t.Context()); err == nil {
		t.Fatal("expired read regained usability")
	}
}
func TestWorkerProcessScopeCloseOwnsOnlyItsObserverFiles(t *testing.T) {
	ownedRead, ownedWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	scope := &nativeWorkerProcessScope{namespace: ownedRead, proc: ownedWrite}
	defer func() { _ = scope.close() }()
	borrowedRead, borrowedWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = borrowedRead.Close() }()
	defer func() { _ = borrowedWrite.Close() }()
	// Ordinary files test partial cleanup ONLY; no genuine namespace/source is
	// asserted, Check is never invoked and no fake positive grant is constructed.
	if err := scope.close(); err != nil {
		t.Fatal(err)
	}
	if err := scope.close(); err != nil {
		t.Fatal("once-close changed result", err)
	}
	if _, err := ownedRead.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatal("owned observer read leaked", err)
	}
	if _, err := ownedWrite.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatal("owned observer write leaked", err)
	}
	if _, err := borrowedRead.Stat(); err != nil {
		t.Fatal("unowned original was closed", err)
	}
	if _, err := borrowedWrite.Stat(); err != nil {
		t.Fatal("unowned original was closed", err)
	}
}
func TestWorkerNativeContextCopyAndEqualityCoverEveryField(t *testing.T) {
	// PURE metadata values, never spaceenv.Configure/Current substitution or a
	// source constructor. Matching DTOs alone cannot establish SDK admission.
	original := &spaceenv.Context{Space: "metadata-only", Mounts: []spaceenv.Mount{{Source: "/workdir/project", Writable: true}}}
	copied := cloneWorkerNativeContext(original)
	original.Mounts[0].Writable = false
	if !copied.Mounts[0].Writable {
		t.Fatal("snapshot aliased original Mounts")
	}
	copied = cloneWorkerNativeContext(original)
	another := copied
	another.Mounts = append([]spaceenv.Mount(nil), copied.Mounts...)
	if sameWorkerNativeContext(original, &another, copied) {
		t.Fatal("equal reconstructed Context pointer adopted")
	}
	for i := 0; i < reflect.TypeOf(*original).NumField(); i++ {
		field := reflect.TypeOf(*original).Field(i).Name
		t.Run(field, func(t *testing.T) {
			changed := cloneWorkerNativeContext(original)
			value := reflect.ValueOf(&changed).Elem().Field(i)
			switch value.Kind() {
			case reflect.String:
				value.SetString(value.String() + "-changed")
			case reflect.Int:
				value.SetInt(value.Int() + 1)
			case reflect.Slice:
				value.Set(reflect.Append(value, reflect.ValueOf(spaceenv.Mount{Source: "/another"})))
			default:
				t.Fatalf("unhandled context field %s", field)
			}
			if sameWorkerNativeContext(original, original, changed) {
				t.Fatal("full Context field change missed", field)
			}
		})
	}
}
func TestWorkerSourceRetainsActualParentUncertaintyAndCancellation(t *testing.T) {
	parent := &workercheckpoint.ParentRefusal{Message: "original receipt storage failed", Uncertain: true}
	err := workerSourceOutcome(errors.Join(parent, context.Canceled))
	var uncertain *NativeEffectsUncertain
	if !errors.As(err, &uncertain) || !errors.Is(err, workercheckpoint.ErrUncertain) || !errors.Is(err, context.Canceled) {
		t.Fatal("typed original uncertainty/cancellation lost", err)
	}
	if errors.As(workerSourceOutcome(errors.New("ordinary unavailable")), &uncertain) {
		t.Fatal("ordinary unavailable became effects uncertainty")
	}
}
