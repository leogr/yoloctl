package openshell

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
)

type retirementCloseFixture struct {
	number int
	closed *[]int
}

func (f retirementCloseFixture) afterContainerStop(context.Context, *nativeContainerStopACK) (pinRetirementProgress, error) {
	return pinRetirementProgress{}, errors.New("close fixture has no effect authority")
}
func (f retirementCloseFixture) Close() error {
	*f.closed = append(*f.closed, f.number)
	return errors.New("fixture close failure")
}

func TestContainerRetirementActualPreparationRequiresOriginalAdmittedScope(t *testing.T) {
	called := 0
	inactive := func(context.Context) error { called++; return nil }
	if p, err := prepareNativeContainerRetirement(t.Context(), nil, inactive); err == nil || p != nil {
		t.Fatal("nil native read scope prepared retirement", p, err)
	}
	if p, err := prepareNativeContainerRetirement(t.Context(), &nativeRemovalReadScope{}, inactive); err == nil || p != nil || called != 0 {
		t.Fatal("ordinary unqualified scope reached native retirement callbacks", p, err, called)
	}
	var r *nativeContainerRetirement
	if r.refreshAfterStop(t.Context(), nil) == nil || r.retireAfterStop(t.Context(), nil) == nil || r.checkStage(t.Context(), nil, pinRetirementProgress{}) == nil {
		t.Fatal("missing parent state became retirement authority")
	}
	r = &nativeContainerRetirement{scope: &nativeRemovalReadScope{closed: true}}
	if r.refreshAfterStop(t.Context(), nil) == nil || r.retireAfterStop(t.Context(), nil) == nil {
		t.Fatal("closed borrowed scope remained a live retirement parent")
	}
}

func TestContainerRetirementClosesAllOwnedHandlesAndPreservesBorrowedScope(t *testing.T) {
	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = directory.Close() }()
	scope := &nativeRemovalReadScope{control: directory}
	var closed []int
	r := &nativeContainerRetirement{scope: scope, entries: []*nativeContainerRetirementEntry{{action: retirementCloseFixture{1, &closed}}, {action: retirementCloseFixture{2, &closed}}}}
	if err := r.Close(); err == nil || !reflect.DeepEqual(closed, []int{2, 1}) {
		t.Fatal("retirement failure left owned preparation handles open", err, closed)
	}
	if err := r.Close(); err != nil || len(closed) != 2 {
		t.Fatal("retirement close repeated handle cleanup", err, closed)
	}
	if _, err := scope.control.Stat(); err != nil || scope.closed {
		t.Fatal("retirement cleanup closed borrowed native scope", err)
	}
	if r.refreshAfterStop(t.Context(), nil) == nil || r.retireAfterStop(t.Context(), nil) == nil {
		t.Fatal("closed parent remained usable")
	}
}
