package openshell

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/leogr/yoloctl/internal/spaceenv"
)

// nilOriginContext is deliberately nil: these tests prove a nil context is refused.
var nilOriginContext context.Context

// No Configure, UID substitution, trusted
// context injection, native-positive seam, filesystem/VM operation or proof.
func TestNativeOriginObservationRefusesOrdinaryExecutorBeforeCallbacks(t *testing.T) {
	if spaceenv.Native() {
		t.Fatal("ordinary-boundary test must not install a synthetic native context")
	}
	checks, histories, uses := 0, 0, 0
	verify := func(context.Context) error { checks++; return nil }
	history := func(NativeOriginIdentity) error { histories++; return nil }
	use := func(*NativeOriginRead) error { uses++; return nil }
	for _, callback := range []func(context.Context) error{nil, verify} {
		if err := WithNativeOriginObservation(t.Context(), "selected", callback, history, use); err == nil {
			t.Fatal("ordinary executor acquired native observation")
		}
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := WithNativeOriginObservation(canceled, "selected", verify, history, use); !errors.Is(err, context.Canceled) {
		t.Fatal("original cancellation not retained", err)
	}
	if err := WithNativeOriginObservation(nilOriginContext, "selected", verify, history, use); err == nil {
		t.Fatal("nil original context accepted")
	}
	if err := WithNativeOriginObservation(t.Context(), "../foreign", verify, history, use); err == nil {
		t.Fatal("invalid independent box accepted")
	}
	if err := WithNativeOriginObservation(t.Context(), "selected", verify, nil, use); err == nil {
		t.Fatal("missing historical qualification callback accepted")
	}
	if err := WithNativeOriginObservation(t.Context(), "selected", verify, history, nil); err == nil {
		t.Fatal("missing borrowed read callback accepted")
	}
	if checks != 0 || histories != 0 || uses != 0 {
		t.Fatal("unadmitted boundary invoked a caller callback", checks, histories, uses)
	}
	if boundary, err := captureNativeOriginObservationBoundary(verify); err == nil || boundary != nil {
		t.Fatal("ordinary current pointer became trusted native boundary")
	}
	if checks != 0 {
		t.Fatal("context capture tried actual storage from ordinary executor")
	}
}

func TestNativeOriginReadZeroExpiredAndFlagAloneCannotYieldMetadata(t *testing.T) {
	for _, read := range []*NativeOriginRead{nil, {}, {active: false}, {active: true}} {
		if err := read.Check(t.Context()); err == nil {
			t.Fatal("zero/expired native origin read accepted")
		}
		if observation, err := read.Observation(t.Context()); err == nil || !reflect.DeepEqual(observation, NativeOriginObservation{}) {
			t.Fatal("invalid scope returned copied absence/classification", err)
		}
		if err := read.Check(nilOriginContext); err == nil {
			t.Fatal("nil context/zero handle accepted")
		}
	}
	var boundary *nativeOriginObservationBoundary
	if err := boundary.check(); err == nil {
		t.Fatal("zero native boundary accepted")
	}
	if err := boundary.verify(nilOriginContext); err == nil {
		t.Fatal("zero boundary/missing context accepted")
	}
}

func TestNativeOriginIdentityCopiesExactlyRecordedSevenFields(t *testing.T) {
	// Deliberately plain metadata, not a valid release or native context. The
	// copy type MUST NOT turn it into protocol/current-helper qualification.
	recorded := containerOrigin{"stored-space", "stored-disk", "stored-uuid", "stored-box", "stored-incarnation", "historical-qualification", "historical-helper"}
	identity := copyNativeOriginIdentity(recorded)
	want := NativeOriginIdentity{"stored-space", "stored-disk", "stored-uuid", "stored-box", "stored-incarnation", "historical-qualification", "historical-helper"}
	if identity != want {
		t.Fatal("stored identity field was substituted")
	}
	identity.Box = "changed-copy"
	if recorded.Box != "stored-box" {
		t.Fatal("copied metadata mutated stored origin")
	}
	fields := reflect.TypeOf(NativeOriginIdentity{})
	if fields.NumField() != 7 {
		t.Fatal("public stored identity added non-recorded fields")
	}
	for _, name := range []string{"Protocol", "HelperProtocol", "ControllerGeneration", "Native", "Admitted"} {
		if _, present := fields.FieldByName(name); present {
			t.Fatal("stored metadata fabricated a non-recorded authority/protocol field", name)
		}
	}
	if NativeOriginPrefixState("") == NativeOriginPrefixPresent || NativeOriginPrefixState("") == NativeOriginMissingRuntime {
		t.Fatal("zero prefix silently classified absence")
	}
}
