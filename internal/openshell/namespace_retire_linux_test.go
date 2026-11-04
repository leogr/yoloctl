package openshell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var namespaceRetireSequence = []string{
	"before-stop", "stop", "check-1", "persist:retiring", "check-2", "retire", "verify-table",
	"persist:table-absent", "check-3", "persist:pin-unmount-attempt", "check-4", "unmount-pin", "verify-pin-gone",
	"persist:unmounted", "check-5", "persist:directory-unmount-attempt", "check-6", "unmount-directory",
	"verify-directory-gone", "persist:directory-unmounted", "check-7", "complete",
}

func namespaceRetireScript(fail string, events *[]string, cause error) pinRetirementHooks {
	step := func(name string) error {
		*events = append(*events, name)
		if name == fail {
			return cause
		}
		return nil
	}
	checks := 0
	return pinRetirementHooks{
		beforeStop: func(context.Context) error { return step("before-stop") },
		stop:       func(context.Context) error { return step("stop") },
		check: func(context.Context, pinRetirementProgress) error {
			checks++
			return step(fmt.Sprintf("check-%d", checks))
		},
		persist:             func(phase namespacePinPhase) error { return step("persist:" + string(phase)) },
		retire:              func(context.Context) error { return step("retire") },
		verifyTable:         func() error { return step("verify-table") },
		unmountPin:          func() error { return step("unmount-pin") },
		verifyPinGone:       func(context.Context) error { return step("verify-pin-gone") },
		unmountDirectory:    func() error { return step("unmount-directory") },
		verifyDirectoryGone: func(context.Context) error { return step("verify-directory-gone") },
		complete: func(p pinRetirementProgress) error {
			if !p.stopAcknowledged || !p.policyAcknowledged || !p.pinUnmountAcknowledged || !p.directoryUnmountAcknowledged || p.completed {
				return errors.New("completion before all positive acknowledgements")
			}
			return step("complete")
		},
	}
}

func TestNamespaceRetirementStopFailureNeverInspectsDeletesOrCompletes(t *testing.T) {
	for index, where := range namespaceRetireSequence {
		t.Run(where, func(t *testing.T) {
			cause := errors.New("lost reply at " + where)
			var events []string
			p, err := runPinRetirement(context.Background(), false, namespaceRetireScript(where, &events, cause))
			var uncertain *NativeEffectsUncertain
			if !errors.Is(err, cause) || errors.As(err, &uncertain) != (index != 0) || p.completed || !reflect.DeepEqual(events, namespaceRetireSequence[:index+1]) {
				t.Fatal("failed boundary continued, suppressed uncertainty, or completed", p, err, events)
			}
			if p.stopAttempted != (index >= 1) || p.stopAcknowledged != (index > 1) || p.policyAttempted != (index >= 5) || p.policyAcknowledged != (index > 5) || p.pinUnmountAttempted != (index >= 11) || p.pinUnmountAcknowledged != (index > 11) || p.directoryUnmountAttempted != (index >= 17) || p.directoryUnmountAcknowledged != (index > 17) {
				t.Fatal("failed reply manufactured an acknowledgement", p, where)
			}
		})
	}
	var events []string
	p, err := runPinRetirement(context.Background(), false, namespaceRetireScript("", &events, nil))
	if err != nil || !p.completed || !reflect.DeepEqual(events, namespaceRetireSequence) {
		t.Fatal(p, err, events)
	}
}

func TestNamespaceRetirementRepeatedStopStillRequiresFreshACKAndInactiveProof(t *testing.T) {
	for _, fail := range []string{"", "stop", "check-1"} {
		var events []string
		cause := errors.New("failed fresh stop/inactive")
		p, err := runPinRetirement(context.Background(), true, namespaceRetireScript(fail, &events, cause))
		end := 3
		if fail == "stop" {
			end = 2
		}
		if !reflect.DeepEqual(events, namespaceRetireSequence[:end]) || p.completed != (fail == "") || (err == nil) != (fail == "") {
			t.Fatal("terminal evidence replayed nft/unmounts or skipped fresh Stop", p, err, events)
		}
	}
}

func TestNamespaceRetirementCancellationBeforeStopVersusBoundedOwnedCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var events []string
	if p, err := runPinRetirement(ctx, false, namespaceRetireScript("", &events, nil)); !errors.Is(err, context.Canceled) || p.stopAttempted || len(events) != 0 {
		t.Fatal(p, err, events)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	hooks := namespaceRetireScript("", &events, nil)
	stop := hooks.stop
	hooks.stop = func(c context.Context) error { err := stop(c); cancel(); return err }
	check := hooks.check
	hooks.check = func(c context.Context, p pinRetirementProgress) error {
		deadline, ok := c.Deadline()
		if c.Err() != nil || !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 30*time.Second {
			return errors.New("cleanup lost its independent finite bound")
		}
		return check(c, p)
	}
	if p, err := runPinRetirement(ctx, false, hooks); err != nil || !p.completed || !reflect.DeepEqual(events, namespaceRetireSequence) {
		t.Fatal(p, err, events)
	}
	if _, err := runPinRetirement(context.Background(), false, pinRetirementHooks{}); err == nil {
		t.Fatal("missing ownership callbacks accepted")
	}
	if _, err := stopNativeNamespacePin(context.Background(), nil, nil, nativePinStopHooks{}); err == nil {
		t.Fatal("missing admitted native owner accepted")
	}
}

func TestNativeEgressRetirementChildRequiresExactBoundedAbsenceProof(t *testing.T) {
	pin, _ := namespacePinRecordFixture(t)
	for _, change := range []string{"", "schema", "namespace", "rules", "present", "unknown", "duplicate", "trailing", "oversize", "empty", "deleted-false"} {
		result := nativeEgressRetirement{BootstrapSchema, pin.Scope.Namespace, pin.Plan.RulesSHA256, true, true}
		switch change {
		case "schema":
			result.Schema++
		case "namespace":
			result.Namespace.Inode++
		case "rules":
			result.RulesSHA256 = strings.Repeat("b", 64)
		case "present":
			result.TableAbsent = false
		case "deleted-false":
			result.Deleted = false // complete current absence can precede any installation
		}
		raw, _ := json.Marshal(result)
		switch change {
		case "unknown":
			raw = append([]byte(`{"extra":true,`), raw[1:]...)
		case "duplicate":
			raw = append([]byte(`{"schema":2,`), raw[1:]...)
		case "trailing":
			raw = append(raw, []byte(`{}`)...)
		case "oversize":
			raw = bytes.Repeat([]byte(" "), 4097)
		case "empty":
			raw = nil
		}
		if err := decodeNativeEgressRetirement(raw, pin.Plan); (err == nil) != (change == "" || change == "deleted-false") {
			t.Fatal(change, err)
		}
	}
}

func syntheticRetirementACKs() pinRetirementProgress {
	// Scripted protocol fixture only, not actual Stop/nft/unmount authority.
	return pinRetirementProgress{stopAcknowledged: true, policyAcknowledged: true, pinUnmountAcknowledged: true, directoryUnmountAcknowledged: true}
}

func TestNamespaceRetirementMarkerRequiresIndependentPublicationAndExactBinding(t *testing.T) {
	_, _, lock, files, pin, record := namespacePinFilesFixture(t)
	if ok, err := files.readRetired(); ok || err != nil {
		t.Fatal(ok, err)
	}
	pin.Phase, pin.DirectoryMountID, pin.PinMountID = pinDirectoryUnmounted, 122, 123
	if err := files.writeObservation(pin); err != nil {
		t.Fatal(err)
	}
	if ok, err := files.readRetired(); ok || err != nil {
		t.Fatal("terminal phase alone became completion", ok, err)
	}
	for _, field := range []string{"stop", "policy", "pin", "directory", "completed"} {
		p := syntheticRetirementACKs()
		switch field {
		case "stop":
			p.stopAcknowledged = false
		case "policy":
			p.policyAcknowledged = false
		case "pin":
			p.pinUnmountAcknowledged = false
		case "directory":
			p.directoryUnmountAcknowledged = false
		case "completed":
			p.completed = true
		}
		if err := files.publishRetired(p); err == nil {
			t.Fatal("partial ACKs published terminal marker", field)
		}
	}
	if err := files.publishRetired(syntheticRetirementACKs()); err != nil {
		t.Fatal(err)
	}
	if err := files.publishRetired(syntheticRetirementACKs()); !errors.Is(err, os.ErrExist) {
		t.Fatal("immutable marker replaced", err)
	}
	cold, err := lock.read(record, pin.Scope.ContainerID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cold.Close() }()
	if ok, err := cold.readRetired(); !ok || err != nil {
		t.Fatal("independent cold marker lost", ok, err)
	}
	pin.Phase = pinUnmounted
	if err := files.writeObservation(pin); err != nil {
		t.Fatal(err)
	}
	if ok, err := files.readRetired(); ok || err == nil {
		t.Fatal("terminal marker survived changed state", ok, err)
	}
}

func TestNamespaceRetirementMarkerRejectsForeignAndUnsafeFiles(t *testing.T) {
	for _, change := range []string{"schema", "origin", "state", "record", "boot", "unknown", "duplicate", "trailing", "link", "mode", "hardlink"} {
		t.Run(change, func(t *testing.T) {
			root, _, lock, files, pin, record := namespacePinFilesFixture(t)
			pin.Phase, pin.DirectoryMountID, pin.PinMountID = pinDirectoryUnmounted, 122, 123
			if err := files.writeObservation(pin); err != nil {
				t.Fatal(err)
			}
			binding, err := pinRetirementBinding(files)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "schema":
				binding.Schema++
			case "origin":
				binding.OriginSHA256 = strings.Repeat("b", 64)
			case "state":
				binding.StateSHA256 = strings.Repeat("b", 64)
			case "record":
				binding.RecordSHA256 = strings.Repeat("b", 64)
			case "boot":
				binding.KernelBootID = "22222222-2222-4333-8444-555555555555"
			}
			raw, _ := json.Marshal(binding)
			switch change {
			case "unknown":
				raw = append([]byte(`{"extra":true,`), raw[1:]...)
			case "duplicate":
				raw = append([]byte(`{"schema":1,`), raw[1:]...)
			case "trailing":
				raw = append(raw, []byte(`{}`)...)
			}
			dir := filepath.Join(root, "namespace-pins", lock.incarnation, "pins", record.Generation)
			name := filepath.Join(dir, "retired.json")
			if err := os.WriteFile(name, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "link":
				if err := os.Remove(name); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("state.json", name); err != nil {
					t.Fatal(err)
				}
			case "mode":
				if err := os.Chmod(name, 0o644); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(name, filepath.Join(dir, "other")); err != nil {
					t.Fatal(err)
				}
			}
			if ok, err := files.readRetired(); ok || err == nil {
				t.Fatal("foreign/unsafe completion accepted", ok, err)
			}
		})
	}
}

func TestNamespaceExistingLockDoesNotInitializeMissingBookkeeping(t *testing.T) {
	for _, missing := range []string{"namespace-pins", "incarnation", "lock", "origins", "pins"} {
		t.Run(missing, func(t *testing.T) {
			root, control, lock, files, _, _ := namespacePinFilesFixture(t)
			inc := lock.incarnation
			if err := files.Close(); err != nil {
				t.Fatal(err)
			}
			if err := lock.Close(); err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(root, "namespace-pins")
			if missing != "namespace-pins" {
				name = filepath.Join(name, inc)
			}
			if missing == "lock" {
				name = filepath.Join(name, ".lock")
			}
			if missing == "origins" || missing == "pins" {
				name = filepath.Join(name, missing)
			}
			if err := os.Rename(name, name+"-retained"); err != nil {
				t.Fatal(err)
			}
			got, err := lockExistingNamespacePinsAt(context.Background(), control, inc, os.Geteuid(), os.Getegid())
			if got != nil {
				_ = got.Close()
			}
			if err == nil {
				t.Fatal("existing-only opener accepted missing bookkeeping")
			}
			if _, err := os.Lstat(name); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("existing-only opener repaired missing bookkeeping", err)
			}
		})
	}
	_, control, lock, files, pin, record := namespacePinFilesFixture(t)
	inc, identity := lock.incarnation, lock.identity
	if err := files.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := lockExistingNamespacePinsAt(context.Background(), control, inc, os.Geteuid(), os.Getegid())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if reopened.identity != identity {
		t.Fatal("existing opener changed stable lock")
	}
	cold, err := reopened.read(record, pin.Scope.ContainerID)
	if err != nil {
		t.Fatal(err)
	}
	_ = cold.Close()
}

func TestNamespaceRetirementObserverStagesCompleteAndRepeatStop(t *testing.T) {
	// Feed the actual inventory validator's output into the same predicate
	// used by the retirement adapter. No mount syscall or native claim.
	target, mounts, directory, namespace := nativePinMountViewFixture(t)
	pin, _ := namespacePinRecordFixture(t)
	pin.DirectoryMountID, pin.PinMountID = 20, 21
	viewAt := func(p pinRetirementProgress, completed bool) (nativePinMountView, error) {
		if completed || p.directoryUnmountAcknowledged {
			directory.mount = 10
			return validateNativePinMountView(mounts[:1], target, 10, directory, pinFileIdentity{directory.device, 102, 10}, false)
		}
		directory.mount = 20
		if p.pinUnmountAcknowledged {
			return validateNativePinMountView(mounts[:2], target, 10, directory, pinFileIdentity{directory.device, 102, 20}, false)
		}
		return validateNativePinMountView(mounts, target, 10, directory, namespace, true)
	}
	for _, repeated := range []bool{false, true} {
		// Repeated Stop uses a real protected marker round-trip, not merely
		// a terminal phase or a scripted unconditional check.
		_, _, _, files, terminal, _ := namespacePinFilesFixture(t)
		completed := false
		if repeated {
			terminal.Phase, terminal.DirectoryMountID, terminal.PinMountID = pinDirectoryUnmounted, 20, 21
			if err := files.writeObservation(terminal); err != nil {
				t.Fatal(err)
			}
			if err := files.publishRetired(syntheticRetirementACKs()); err != nil {
				t.Fatal(err)
			}
			var err error
			completed, err = files.readRetired()
			if err != nil || !completed {
				t.Fatal("protected terminal marker not established", completed, err)
			}
		}
		check := func(p pinRetirementProgress) error {
			view, err := viewAt(p, completed)
			if err != nil {
				return err
			}
			return validatePinRetirementStage(view, pin, 10, completed, p)
		}
		var events []string
		h := namespaceRetireScript("", &events, nil)
		h.beforeStop = func(context.Context) error { return check(pinRetirementProgress{}) }
		h.check = func(_ context.Context, p pinRetirementProgress) error { return check(p) }
		h.verifyPinGone = func(context.Context) error { return check(pinRetirementProgress{pinUnmountAcknowledged: true}) }
		h.verifyDirectoryGone = func(context.Context) error {
			return check(pinRetirementProgress{pinUnmountAcknowledged: true, directoryUnmountAcknowledged: true})
		}
		p, err := runPinRetirement(context.Background(), completed, h)
		if err != nil || !p.completed || !p.stopAcknowledged {
			t.Fatal("real observer stage refused successful retirement/repeated Stop", repeated, p, err)
		}
		if repeated && (len(events) != 1 || events[0] != "stop") {
			t.Fatal("repeated Stop replayed cleanup", events)
		}
	}
}

func TestNamespaceRetirementObserverStagesRefuseWrongBaseBoundAndResidualMounts(t *testing.T) {
	target, mounts, directory, namespace := nativePinMountViewFixture(t)
	pin, _ := namespacePinRecordFixture(t)
	pin.DirectoryMountID, pin.PinMountID = 20, 21
	pinned, err := validateNativePinMountView(mounts, target, 10, directory, namespace, true)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := validateNativePinMountView(mounts[:2], target, 10, directory, pinFileIdentity{directory.device, 102, 20}, false)
	if err != nil {
		t.Fatal(err)
	}
	directory.mount = 10
	bare, err := validateNativePinMountView(mounts[:1], target, 10, directory, pinFileIdentity{directory.device, 102, 10}, false)
	if err != nil || bare.directoryMount != 10 || bare.directoryBound {
		t.Fatal("actual bare observer contract changed", bare, err)
	}
	foreignMounts := append([]nativePinMount(nil), mounts[:2]...)
	foreignMounts[1].id = 30
	directory.mount = 30
	foreignBound, err := validateNativePinMountView(foreignMounts, target, 10, directory, pinFileIdentity{directory.device, 102, 30}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		view      nativePinMountView
		base      uint64
		completed bool
		progress  pinRetirementProgress
	}{
		{"remaining-bind", bound, 10, true, pinRetirementProgress{}},
		{"foreign-bind", foreignBound, 10, true, pinRetirementProgress{}},
		{"wrong-base", bare, 11, true, pinRetirementProgress{}},
		{"remaining-pin", pinned, 10, true, pinRetirementProgress{}},
		{"pin-after-unmount", pinned, 10, false, pinRetirementProgress{pinUnmountAcknowledged: true}},
		{"bare-before-ack", bare, 10, false, pinRetirementProgress{}},
		{"foreign-bound-before-unmount", foreignBound, 10, false, pinRetirementProgress{pinUnmountAcknowledged: true}},
		{"bound-after-directory-ack", bound, 10, false, pinRetirementProgress{pinUnmountAcknowledged: true, directoryUnmountAcknowledged: true}},
	} {
		if err := validatePinRetirementStage(test.view, pin, test.base, test.completed, test.progress); err == nil {
			t.Fatal("foreign/residual stage accepted", test.name)
		}
	}
}
