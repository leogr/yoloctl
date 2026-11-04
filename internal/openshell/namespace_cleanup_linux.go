package openshell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"golang.org/x/sys/unix"
)

type namespaceCleanupStep struct {
	parent               *os.File // borrowed from the original held scope
	parentKey, name, key string
	directory            bool
	closeRemoved         func() error
}

// The actual constructor below runs only after this call's all-generation
// retirement. Neither an incomplete raw listing nor a terminal phase can
// construct its removal ACK. Stable lock/collection parents are never steps.
type nativeNamespaceCleanup struct {
	retirement           *nativeContainerRetirement // borrowed
	lock                 *namespacePinLock
	records              []RuntimeRecord
	cid                  string
	expected             *namespaceCleanupInventory
	steps                []namespaceCleanupStep
	flexibleSize         map[string]bool
	attempted, completed bool
	failure              error
}

func verifyNativeNamespaceCleanupBare(ctx context.Context, scope *nativeRemovalReadScope) error {
	if scope == nil || scope.closed || scope.kernel == nil || scope.control == nil {
		return errors.New("namespace cleanup lacks original admitted infrastructure")
	}
	if !scope.selected.Launch.UnconfinedNetwork {
		return verifyConfinedNamespaceAbsence(scope.control, scope.controlIdentity, scope.selected.Launch.Binding.Incarnation)
	}
	if scope.lock == nil {
		return errors.New("namespace cleanup lost original ownership lock")
	}
	if err := scope.lock.check(); err != nil {
		return err
	}
	control := scope.control.Name()
	if !filepath.IsAbs(control) || filepath.Clean(control) != control {
		return errors.New("namespace cleanup has no canonical original control path")
	}
	target := filepath.Join(control, "namespace-pins", scope.lock.incarnation)
	mounts, err := readNativePinMountInventory(ctx, scope.kernel)
	if err != nil {
		return err
	}
	return validateNoInitializationMounts(mounts, target, scope.lock.directories[2].identity)
}

func compareCleanupToComplete(cleanup *namespaceCleanupInventory, complete *namespaceGenerationInventory) error {
	if cleanup == nil || complete == nil || !reflect.DeepEqual(cleanup.members, complete.members) {
		return errors.New("cleanup snapshot differs from complete acknowledged namespace inventory")
	}
	raw := map[string][]byte{}
	for _, f := range complete.pins {
		raw["origins/"+f.record.Generation+".json"] = f.origin
		prefix := "pins/" + f.record.Generation + "/"
		raw[prefix+"namespace"] = []byte{}
		raw[prefix+"state.json"] = f.current
		if _, exists := complete.members[prefix+"retired.json"]; exists {
			binding, err := pinRetirementBinding(f)
			if err != nil {
				return err
			}
			marker, err := json.Marshal(binding)
			if err != nil {
				return err
			}
			raw[prefix+"retired.json"] = marker
		}
	}
	for _, f := range complete.noInitialization {
		found := false
		for _, record := range complete.records {
			if recordIdentity(record) == f.marker.RecordSHA256 {
				raw[noInitializationDirectory+"/"+record.Generation+".json"] = f.raw
				found = true
				break
			}
		}
		if !found {
			return errors.New("cleanup no-init marker lost original record")
		}
	}
	// Empty placeholder bytes may be nil or non-nil after a bounded read.
	if len(raw) != len(cleanup.raw) {
		return errors.New("cleanup snapshot changed complete raw member coverage")
	}
	for key, expected := range raw {
		observed, exists := cleanup.raw[key]
		if !exists || !bytes.Equal(expected, observed) {
			return errors.New("cleanup snapshot changed complete original/producer bytes")
		}
	}
	return nil
}

func prepareNativeNamespaceCleanup(ctx context.Context, r *nativeContainerRetirement, ack *nativeContainerStopACK) (*nativeNamespaceCleanup, error) {
	if r == nil || r.closed || r.scope == nil || r.full == nil || !r.retired {
		return nil, errors.New("namespace cleanup requires original all-generation retirement")
	}
	if err := ack.coversInventory(r.full, r.scope.cid); err != nil {
		return nil, err
	}
	if err := r.scope.checkRuntime(ctx); err != nil {
		return nil, err
	}
	if err := r.full.Check(ctx); err != nil {
		return nil, err
	}
	if err := verifyNativeNamespaceCleanupBare(ctx, r.scope); err != nil {
		return nil, err
	}
	p := &nativeNamespaceCleanup{retirement: r, records: r.full.Records(), cid: r.scope.cid, flexibleSize: map[string]bool{}}
	if !r.scope.selected.Launch.UnconfinedNetwork {
		return p, nil
	}
	if err := compareCurrentRetirementNamespaceStage(ctx, r.scope.namespaces, r.entries, nil, pinRetirementProgress{}, r.scope.lock.directories[4].identity.mount); err != nil {
		return nil, err
	}
	current, err := r.scope.namespaces.readCurrent(ctx)
	if err != nil {
		return nil, err
	}
	defer current.Close()
	if err := compareRetirementNamespaceStage(r.scope.namespaces, current, r.entries, nil, pinRetirementProgress{}, r.scope.lock.directories[4].identity.mount); err != nil {
		return nil, err
	}
	observed, err := inventoryNamespaceCleanup(ctx, r.scope.lock, p.records)
	if err != nil {
		return nil, err
	}
	if err := compareCleanupToComplete(observed, current); err != nil {
		return nil, err
	}
	p.lock = r.scope.lock
	p.expected = observed
	if err := populateNamespaceCleanupSteps(p, r.entries, r.scope.namespaces.noInitialization); err != nil {
		return nil, err
	}
	// Recheck the COMPLETE original+producer comparison after capture. The
	// captured listing is observation, not a replacement ownership decision.
	if err := compareCurrentRetirementNamespaceStage(ctx, r.scope.namespaces, r.entries, nil, pinRetirementProgress{}, r.scope.lock.directories[4].identity.mount); err != nil {
		return nil, err
	}
	return p, nil
}

// The actual constructor first compares the complete original+producer scope.
// This fixed-step builder is shared with owned-file protocol fixtures only.
func populateNamespaceCleanupSteps(p *nativeNamespaceCleanup, entries []*nativeContainerRetirementEntry, noinit []*noInitializationFiles) error {
	if p == nil || p.lock == nil || p.expected == nil || len(p.steps) != 0 || len(entries) != len(p.records) {
		return errors.New("namespace cleanup step plan lacks original complete selection")
	}
	if err := p.lock.check(); err != nil {
		return err
	}
	observed := p.expected
	complete := map[string]string{}
	for _, record := range p.records {
		if complete[record.Generation] != "" {
			return errors.New("namespace cleanup repeated original generation")
		}
		complete[record.Generation] = recordIdentity(record)
	}
	markers := map[string]*noInitializationFiles{}
	for _, f := range noinit {
		if f == nil || f.lock != p.lock || markers[f.marker.RecordSHA256] != nil {
			return errors.New("namespace cleanup has invalid/repeated no-init selection")
		}
		markers[f.marker.RecordSHA256] = f
	}
	for _, e := range entries {
		if e == nil || complete[e.record.Generation] != recordIdentity(e.record) {
			return errors.New("namespace cleanup changed complete original step records")
		}
		delete(complete, e.record.Generation)
		marker := markers[recordIdentity(e.record)]
		if (e.pin == nil) == (marker == nil) {
			return errors.New("namespace cleanup has missing/conflicting original group")
		}
		if e.pin != nil && (e.pin.lock != p.lock || e.pin.cid != p.cid || recordIdentity(e.pin.record) != recordIdentity(e.record)) {
			return errors.New("namespace cleanup pin belongs to another original record/CID")
		}
		delete(markers, recordIdentity(e.record))
	}
	if len(complete) != 0 || len(markers) != 0 {
		return errors.New("namespace cleanup omitted original groups")
	}
	// Remove all generation data first; keep external origins and independent
	// no-init completion anchors until every pin directory has gone.
	for _, e := range entries {
		if e.pin == nil {
			continue
		}
		prefix := "pins/" + e.record.Generation
		for _, name := range []string{"namespace", "retired.json", "state.json"} {
			if _, exists := observed.members[prefix+"/"+name]; exists {
				p.steps = append(p.steps, namespaceCleanupStep{parent: e.pin.directory, parentKey: prefix, name: name, key: prefix + "/" + name})
			}
		}
		p.steps = append(p.steps, namespaceCleanupStep{parent: p.lock.directories[4].file, parentKey: "pins", name: e.record.Generation, key: prefix, directory: true, closeRemoved: e.pin.Close})
	}
	for _, e := range entries {
		if e.pin != nil {
			name := e.record.Generation + ".json"
			p.steps = append(p.steps, namespaceCleanupStep{parent: p.lock.directories[3].file, parentKey: "origins", name: name, key: "origins/" + name})
		}
	}
	for _, f := range noinit {
		found := false
		for _, record := range p.records {
			if recordIdentity(record) == f.marker.RecordSHA256 {
				name := record.Generation + ".json"
				p.steps = append(p.steps, namespaceCleanupStep{parent: f.directory, parentKey: noInitializationDirectory, name: name, key: noInitializationDirectory + "/" + name, closeRemoved: f.Close})
				found = true
				break
			}
		}
		if !found {
			return errors.New("namespace cleanup omitted original no-init record")
		}
	}
	return nil
}

func (p *nativeNamespaceCleanup) checkExpected(ctx context.Context) error {
	if p == nil || p.lock == nil || p.expected == nil {
		return errors.New("namespace cleanup lacks retained original snapshot")
	}
	current, err := inventoryNamespaceCleanup(ctx, p.lock, p.records)
	if err != nil {
		return err
	}
	if len(current.members) != len(p.expected.members) || len(current.raw) != len(p.expected.raw) {
		return errors.New("namespace cleanup changed expected remaining member coverage")
	}
	for key, expected := range p.expected.members {
		observed, exists := current.members[key]
		if p.flexibleSize[key] {
			expected.size = observed.size
		}
		if !exists || expected != observed {
			return errors.New("namespace cleanup changed original remaining identity/role/links")
		}
	}
	for key, expected := range p.expected.raw {
		observed, exists := current.raw[key]
		if !exists || !bytes.Equal(expected, observed) {
			return errors.New("namespace cleanup changed original remaining bytes")
		}
	}
	return nil
}

func (p *nativeNamespaceCleanup) verifyStep(step namespaceCleanupStep) error {
	if p == nil || p.lock == nil || p.expected == nil || step.parent == nil || step.name == "" || step.name == "." || step.name == ".." || strings.ContainsAny(step.name, "/\\\x00") || step.key != step.parentKey+"/"+step.name {
		return errors.New("namespace cleanup step lost original fixed parent/member")
	}
	parent, exists := p.expected.members[step.parentKey]
	if !exists {
		return errors.New("namespace cleanup cannot remove stable collection/lock parents")
	}
	identity, err := pinIdentity(int(step.parent.Fd()), p.lock.uid, p.lock.gid, true)
	if err != nil {
		return err
	}
	if identity != parent.identity {
		return errors.New("namespace cleanup selected foreign parent descriptor")
	}
	expected, exists := p.expected.members[step.key]
	if !exists {
		return errors.New("namespace cleanup step is outside expected original remaining set")
	}
	observed, err := namespaceInventoryMemberAt(int(step.parent.Fd()), step.name)
	if err != nil {
		return err
	}
	if p.flexibleSize[step.key] {
		expected.size = observed.size
	}
	if expected != observed {
		return errors.New("namespace cleanup target changed original identity/role/links")
	}
	if step.directory {
		if expected.mode != unix.S_IFDIR|0700 {
			return errors.New("namespace cleanup target is not its original generation directory")
		}
		for key := range p.expected.members {
			if strings.HasPrefix(key, step.key+"/") {
				return errors.New("namespace cleanup cannot remove directory before original children")
			}
		}
	} else {
		raw, exists := p.expected.raw[step.key]
		if !exists {
			return errors.New("namespace cleanup target lacks original bytes")
		}
		current, id, err := readPinFileAt(int(step.parent.Fd()), step.name, p.lock.uid, p.lock.gid, int64(len(raw)))
		if err != nil {
			return err
		}
		if id != expected.identity || !bytes.Equal(raw, current) {
			return errors.New("namespace cleanup target changed original bytes")
		}
	}
	return nil
}

// Invoked only as the original protocol's namespaceMetadata callback, after
// actual rm and complete remaining-consumer proof. Every unlink and fsync is
// one attempted effect; failure stops the suffix without readback-as-success.
func (p *nativeNamespaceCleanup) removeAfterACK(ctx context.Context, ack *nativeContainerRemoveACK, remaining func(context.Context) error) error {
	if p != nil && p.attempted {
		return p.removeWithCurrentVerifier(ctx, ack, nil)
	}
	if p == nil || p.retirement == nil || p.retirement.closed || !p.retirement.retired || p.retirement.scope == nil || remaining == nil {
		return errors.New("namespace cleanup lacks original live scope/current verifier")
	}
	return p.removeWithCurrentVerifier(ctx, ack, func(c context.Context) error {
		if err := p.retirement.scope.checkInfrastructure(c, remaining); err != nil {
			return err
		}
		if err := p.retirement.full.Check(c); err != nil {
			return err
		}
		return verifyNativeNamespaceCleanupBare(c, p.retirement.scope)
	})
}

// Shared original-ACK file protocol, with actual current native predicates
// supplied only by removeAfterACK above. Project fixtures use owned files and
// scripted current predicates; they do not acquire native runtime authority.
func (p *nativeNamespaceCleanup) removeWithCurrentVerifier(ctx context.Context, ack *nativeContainerRemoveACK, current func(context.Context) error) (err error) {
	if p != nil && p.attempted {
		if p.failure != nil {
			return p.failure
		}
		return &NativeEffectsUncertain{Cause: errors.New("namespace metadata cleanup already attempted; no retry")}
	}
	if p == nil || p.retirement == nil || p.retirement.closed || !p.retirement.retired || current == nil {
		return errors.New("namespace cleanup lacks original live removal/current verifier")
	}
	if p.retirement.full == nil {
		return errors.New("namespace cleanup lost original full runtime inventory")
	}
	if err := ack.coversInventory(p.retirement.full, p.cid, false); err != nil {
		return err
	}
	if len(p.records) == 0 || len(p.records) != len(p.retirement.full.records) {
		return errors.New("namespace cleanup omitted original records")
	}
	complete := map[string]string{}
	for _, record := range p.retirement.full.records {
		complete[record.Generation] = recordIdentity(record)
	}
	for _, record := range p.records {
		if complete[record.Generation] != recordIdentity(record) {
			return errors.New("namespace cleanup changed original complete record set")
		}
		delete(complete, record.Generation)
	}
	if len(complete) != 0 {
		return errors.New("namespace cleanup lost original record coverage")
	}

	p.attempted = true
	defer func() {
		if err != nil {
			err = &NativeEffectsUncertain{Cause: err}
			p.failure = err
		}
	}()
	verify := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := current(ctx); err != nil {
			return err
		}
		if p.lock != nil {
			return p.checkExpected(ctx)
		}
		if len(p.steps) != 0 || p.expected != nil {
			return errors.New("confined cleanup acquired namespace deletion steps")
		}
		for _, record := range p.records {
			if record.Launch.UnconfinedNetwork {
				return errors.New("unconfined cleanup lost original namespace lock/snapshot")
			}
		}
		return nil
	}
	if err := verify(); err != nil {
		return err
	}
	for _, step := range p.steps {
		if err := verify(); err != nil {
			return err
		}
		if err := p.verifyStep(step); err != nil {
			return err
		}
		flags := 0
		if step.directory {
			flags = unix.AT_REMOVEDIR
		}
		if err := unix.Unlinkat(int(step.parent.Fd()), step.name, flags); err != nil {
			return err
		}
		if err := step.parent.Sync(); err != nil {
			return err
		}
		// Only this successful unlink+fsync advances expected remaining data.
		delete(p.expected.members, step.key)
		delete(p.expected.raw, step.key)
		p.flexibleSize[step.parentKey] = true
		if step.directory {
			parent, exists := p.expected.members[step.parentKey]
			if !exists || parent.links <= 2 {
				return errors.New("namespace directory removal lost original parent link accounting")
			}
			parent.links--
			p.expected.members[step.parentKey] = parent
		}
		if step.closeRemoved != nil {
			if err := step.closeRemoved(); err != nil {
				return err
			}
		}
	}
	if err := verify(); err != nil {
		return err
	}
	p.completed = true
	return nil
}
