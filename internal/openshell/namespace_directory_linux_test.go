package openshell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

var directoryCreateSequence = []string{
	"check-1", "persist:directory-bind-attempt:0", "check-2", "bind", "reopen",
	"persist:directory-bound:17", "check-3", "persist:directory-private-attempt:17",
	"check-4", "private", "verify-private", "persist:directory-private:17",
}

func pinDirectoryScript(fail string, events *[]string, cause error) pinDirectoryHooks {
	step := func(name string) error {
		*events = append(*events, name)
		if name == fail {
			return cause
		}
		return nil
	}
	checks := 0
	return pinDirectoryHooks{
		check: func() error { checks++; return step(fmt.Sprintf("check-%d", checks)) },
		persist: func(phase namespacePinPhase, mount uint64) error {
			return step(fmt.Sprintf("persist:%s:%d", phase, mount))
		},
		bind: func() error { return step("bind") },
		reopenBound: func() (uint64, error) {
			if err := step("reopen"); err != nil {
				return 0, err
			}
			if fail == "reopen-zero" {
				return 0, nil
			}
			return 17, nil
		},
		makePrivate:   func() error { return step("private") },
		verifyPrivate: func() error { return step("verify-private") },
	}
}

func TestNamespacePinDirectoryProtocolOrdersDurableAttemptsAndActualACKs(t *testing.T) {
	var events []string
	progress, err := runPinDirectoryCreate(context.Background(), pinDirectoryScript("", &events, nil))
	if err != nil || !progress.ready || !progress.bindAttempted || !progress.bindAcknowledged || !progress.boundRecorded || progress.directoryMount != 17 || !progress.privateAttempted || !progress.privateAcknowledged || !reflect.DeepEqual(events, directoryCreateSequence) {
		t.Fatalf("directory protocol progress/order: %+v, %v, %q", progress, err, events)
	}
	// Scripted callbacks establish no real mount or opaque native token.
}

func TestNamespacePinDirectoryProtocolNeverContinuesAfterFailedReply(t *testing.T) {
	for index, where := range directoryCreateSequence {
		t.Run(where, func(t *testing.T) {
			var events []string
			cause := errors.New("lost reply at " + where)
			progress, err := runPinDirectoryCreate(context.Background(), pinDirectoryScript(where, &events, cause))
			var uncertain *NativeEffectsUncertain
			wantUncertain := !strings.HasPrefix(where, "check-")
			if err == nil || !errors.Is(err, cause) || errors.As(err, &uncertain) != wantUncertain || progress.ready || !reflect.DeepEqual(events, directoryCreateSequence[:index+1]) {
				t.Fatalf("failed reply advanced/reclassified: %+v, %v, %q", progress, err, events)
			}
			if progress.bindAttempted != (index >= 3) || progress.bindAcknowledged != (index > 3) || progress.boundRecorded != (index > 5) || progress.privateAttempted != (index >= 9) || progress.privateAcknowledged != (index > 9) {
				t.Fatalf("observation/attempt manufactured a positive ACK: %+v at %s", progress, where)
			}
		})
	}
}

func TestNamespacePinDirectoryProtocolRequiresExactReadbackAndRetainsNestedUncertainty(t *testing.T) {
	var events []string
	progress, err := runPinDirectoryCreate(context.Background(), pinDirectoryScript("reopen-zero", &events, nil))
	var uncertain *NativeEffectsUncertain
	if !errors.As(err, &uncertain) || progress.directoryMount != 0 || progress.boundRecorded || progress.ready || !reflect.DeepEqual(events, directoryCreateSequence[:5]) {
		t.Fatalf("zero mount readback accepted or continued: %+v, %v, %q", progress, err, events)
	}
	events = nil
	cause := &NativeEffectsUncertain{Cause: errors.New("storage uncertainty")}
	progress, err = runPinDirectoryCreate(context.Background(), pinDirectoryScript("check-3", &events, cause))
	if !errors.Is(err, cause) || !errors.As(err, &uncertain) || !progress.boundRecorded || progress.privateAttempted || progress.ready || !reflect.DeepEqual(events, directoryCreateSequence[:7]) {
		t.Fatalf("nested uncertainty lost before next effect: %+v, %v, %q", progress, err, events)
	}
}

func TestNamespacePinDirectoryProtocolCancellationAndMissingInputsHaveNoEffects(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var events []string
	if progress, err := runPinDirectoryCreate(ctx, pinDirectoryScript("", &events, nil)); !errors.Is(err, context.Canceled) || progress.ready || len(events) != 0 {
		t.Fatal("canceled protocol reached a callback", progress, err, events)
	}
	if progress, err := runPinDirectoryCreate(context.Background(), pinDirectoryHooks{}); err == nil || progress.ready {
		t.Fatal("missing effect callbacks accepted", progress, err)
	}
}

func TestNamespacePinDirectoryFreshEntryRefusesMissingConsumedOrClosedProofs(t *testing.T) {
	if attempt, err := createNativePinDirectory(context.Background(), nil, nil, nil); err == nil || attempt != nil {
		t.Fatal("missing reservation/peer/kernel accepted")
	}
	if err := verifyPinProcPath(nil); err == nil {
		t.Fatal("missing kernel proc accepted")
	}
	file, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	files := &namespacePinFiles{directory: file}
	r := &namespacePinReservation{files: files}
	// Ordinary descriptor fixture, not an exclusive native reservation or ACK.
	if attempt, err := createNativePinDirectory(context.Background(), nil, r, nil); err == nil || attempt != nil || r.consumed {
		t.Fatal("metadata alone minted a fresh directory attempt")
	}
	r.consumed = true
	if attempt, err := createNativePinDirectory(context.Background(), nil, r, nil); err == nil || attempt != nil {
		t.Fatal("consumed token reused")
	}
	attempt := &nativePinDirectoryAttempt{reservation: r}
	if err := attempt.Close(); err != nil || !attempt.closed {
		t.Fatal("attempt Close failed", err)
	}
	if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatal("attempt Close leaked metadata FD", err)
	}
	if err := attempt.Close(); err != nil {
		t.Fatal("attempt Close not idempotent", err)
	}
	if next, err := createNativePinDirectory(context.Background(), nil, r, nil); err == nil || next != nil {
		t.Fatal("closed token reused")
	}
}
