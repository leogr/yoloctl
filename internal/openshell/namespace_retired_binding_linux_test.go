package openshell

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// Ordinary source metadata produced by the existing reserve/writeObservation
// fixture ONLY. Terminal phases below are synthetic stored shapes, never real
// mount/Stop/unmount/remove ACKs. No namespace is opened, adopted or entered.
func namespaceRetiredBindingFixture(t *testing.T, phase namespacePinPhase) (*namespacePinFiles, RuntimeRecord, string, []byte, []byte) {
	t.Helper()
	_, _, _, files, state, record := namespacePinFilesFixture(t)
	state.Phase = phase
	switch phase {
	case pinDirectoryUnmounted:
		state.DirectoryMountID, state.PinMountID = 122, 123
	case pinPartialUnmounted:
		state.DirectoryMountID = 122
	}
	if err := files.writeObservation(state); err != nil {
		t.Fatal(err)
	}
	return files, record, state.Scope.ContainerID, bytes.Clone(files.origin), bytes.Clone(files.current)
}
func namespaceRetiredBindingMarshal(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestNamespaceRetiredBindingMatchesOriginalStoredBytesAndOldWrappers(t *testing.T) {
	for _, phase := range []namespacePinPhase{pinDirectoryUnmounted, pinPlanned, pinDirectoryBindAttempt, pinPartialUnmounted} {
		t.Run(string(phase), func(t *testing.T) {
			files, record, cid, originRaw, stateRaw := namespaceRetiredBindingFixture(t, phase)
			got, err := namespaceRetiredBindingFromMetadata(originRaw, stateRaw, record, cid)
			if err != nil {
				t.Fatal(err)
			}
			schema := 2
			if phase == pinDirectoryUnmounted {
				schema = 1
			}
			var state namespacePinRecord
			if err := json.Unmarshal(stateRaw, &state); err != nil {
				t.Fatal(err)
			}
			expected := namespacePinRetired{schema, sha256Hex(originRaw), sha256Hex(stateRaw), recordIdentity(record), state.Scope.KernelBootID}
			if got != expected {
				t.Fatal("changed schema or exact stored byte joins", got)
			}
			old, err := pinRetirementBinding(files)
			if err != nil || old != expected {
				t.Fatal("old live-handle wrapper changed", old, err)
			}
			marker := namespaceRetiredBindingMarshal(t, got)
			decoded, err := decodeNamespaceRetiredMetadata(marker, expected)
			if err != nil || decoded != expected {
				t.Fatal(decoded, err)
			}
			if err := validateRetiredPinMarker(marker, files); err != nil {
				t.Fatal("old effect decoder changed", err)
			}
			// Decoded metadata has no mutable aliases into the caller's input bytes.
			saved := decoded
			for i := range marker {
				marker[i] = 'x'
			}
			if decoded != saved {
				t.Fatal("metadata result aliases raw bytes")
			}
		})
	}
}
func TestNamespaceRetiredBindingMatchesActualProtectedFileProducer(t *testing.T) {
	files, record, cid, originRaw, stateRaw := namespaceRetiredBindingFixture(t, pinDirectoryUnmounted)
	actualOrigin, _, err := readPinFileAt(int(files.lock.directories[3].file.Fd()), record.Generation+".json", files.lock.uid, files.lock.gid, 4<<20)
	if err != nil || !bytes.Equal(actualOrigin, originRaw) {
		t.Fatal("original producer origin differs", err)
	}
	actualState, _, err := readPinFileAt(int(files.directory.Fd()), "state.json", files.lock.uid, files.lock.gid, 4<<20)
	if err != nil || !bytes.Equal(actualState, stateRaw) {
		t.Fatal("actual stored state differs", err)
	}
	binding, err := namespaceRetiredBindingFromMetadata(actualOrigin, actualState, record, cid)
	if err != nil {
		t.Fatal(err)
	}
	// This direct ordinary-fixture write exercises only the existing0600 JSON
	// encoding. It deliberately does not call/pretend to satisfy publishRetired.
	if err := writePinFileAt(int(files.directory.Fd()), "retired.json", namespaceRetiredBindingMarshal(t, binding), false, files.lock.uid, files.lock.gid); err != nil {
		t.Fatal(err)
	}
	raw, _, err := readPinFileAt(int(files.directory.Fd()), "retired.json", files.lock.uid, files.lock.gid, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeNamespaceRetiredMetadata(raw, binding); err != nil {
		t.Fatal(err)
	}
	if err := validateRetiredPinMarker(raw, files); err != nil {
		t.Fatal(err)
	}
}
func TestNamespaceRetiredBindingSourceValidationIsClosedAndZeroOnError(t *testing.T) {
	_, record, cid, originRaw, stateRaw := namespaceRetiredBindingFixture(t, pinDirectoryUnmounted)
	for _, which := range []string{"nil-origin", "large-origin", "null-origin", "origin-alias", "origin-duplicate", "origin-trailing", "origin-space", "origin-bad-directory", "origin-not-planned", "nil-state", "large-state", "null-state", "state-alias-first", "state-alias-last", "state-duplicate", "state-trailing", "state-space", "state-foreign-origin", "state-foreign-scope", "state-nonterminal", "cid", "record"} {
		t.Run(which, func(t *testing.T) {
			origin, state := bytes.Clone(originRaw), bytes.Clone(stateRaw)
			selected, selectedCID := cloneRuntimeRecord(record), cid
			var o namespacePinOrigin
			if err := json.Unmarshal(origin, &o); err != nil {
				t.Fatal(err)
			}
			var s namespacePinRecord
			if err := json.Unmarshal(state, &s); err != nil {
				t.Fatal(err)
			}
			switch which {
			case "nil-origin":
				origin = nil
			case "large-origin":
				origin = make([]byte, (4<<20)+1)
			case "null-origin":
				origin = []byte("null")
			case "origin-alias":
				origin = []byte(strings.Replace(string(origin), `"directory":`, `"Directory":`, 1))
			case "origin-duplicate":
				origin = append([]byte(`{"directory":`+string(namespaceRetiredBindingMarshal(t, o.Directory))+`,`), origin[1:]...)
			case "origin-trailing":
				origin = append(origin, []byte(" {}")...)
			case "origin-space":
				origin = append(origin, ' ')
			case "origin-bad-directory":
				o.Directory.Inode = 0
				origin = namespaceRetiredBindingMarshal(t, o)
			case "origin-not-planned":
				o.Record = s
				origin = namespaceRetiredBindingMarshal(t, o)
			case "nil-state":
				state = nil
			case "large-state":
				state = make([]byte, (4<<20)+1)
			case "null-state":
				state = []byte("null")
			case "state-alias-first":
				state = append([]byte(`{"Schema":1,`), state[1:]...)
			case "state-alias-last":
				state = append(append([]byte(nil), state[:len(state)-1]...), []byte(`,"Schema":1}`)...)
			case "state-duplicate":
				state = append([]byte(`{"schema":1,`), state[1:]...)
			case "state-trailing":
				state = append(state, []byte(" {}")...)
			case "state-space":
				state = append(state, ' ')
			case "state-foreign-origin":
				s.OriginSHA256 = strings.Repeat("c", 64)
				state = namespaceRetiredBindingMarshal(t, s)
			case "state-foreign-scope":
				s.Scope.PeerStartTicks = "54321"
				state = namespaceRetiredBindingMarshal(t, s)
			case "state-nonterminal":
				s.Phase = pinUnmounted
				state = namespaceRetiredBindingMarshal(t, s)
			case "cid":
				selectedCID = strings.Repeat("b", 64)
			case "record":
				selected.Launch.Binding.Box = "foreign"
			}
			got, err := namespaceRetiredBindingFromMetadata(origin, state, selected, selectedCID)
			if err == nil || got != (namespacePinRetired{}) {
				t.Fatal("bad input returned usable metadata", got, err)
			}
		})
	}
}
func TestNamespaceRetiredMarkerStrictDecoderBindsExactOriginalSource(t *testing.T) {
	_, record, cid, originRaw, stateRaw := namespaceRetiredBindingFixture(t, pinDirectoryUnmounted)
	expected, err := namespaceRetiredBindingFromMetadata(originRaw, stateRaw, record, cid)
	if err != nil {
		t.Fatal(err)
	}
	raw := namespaceRetiredBindingMarshal(t, expected)
	for _, which := range []string{"nil", "large", "null", "schema", "origin", "state", "record", "boot", "duplicate", "alias-first", "alias-last", "unknown", "trailing", "space"} {
		t.Run(which, func(t *testing.T) {
			changed := bytes.Clone(raw)
			value := expected
			switch which {
			case "nil":
				changed = nil
			case "large":
				changed = make([]byte, 4097)
			case "null":
				changed = []byte("null")
			case "schema":
				value.Schema = 2
				changed = namespaceRetiredBindingMarshal(t, value)
			case "origin":
				value.OriginSHA256 = strings.Repeat("c", 64)
				changed = namespaceRetiredBindingMarshal(t, value)
			case "state":
				value.StateSHA256 = strings.Repeat("c", 64)
				changed = namespaceRetiredBindingMarshal(t, value)
			case "record":
				value.RecordSHA256 = strings.Repeat("c", 64)
				changed = namespaceRetiredBindingMarshal(t, value)
			case "boot":
				value.KernelBootID = "22222222-2222-4333-8444-555555555555"
				changed = namespaceRetiredBindingMarshal(t, value)
			case "duplicate":
				changed = append([]byte(`{"schema":1,`), raw[1:]...)
			case "alias-first":
				changed = append([]byte(`{"Schema":1,`), raw[1:]...)
			case "alias-last":
				changed = append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"Schema":1}`)...)
			case "unknown":
				changed = append([]byte(`{"unknown":true,`), raw[1:]...)
			case "trailing":
				changed = append(changed, []byte(" {}")...)
			case "space":
				changed = append(changed, ' ')
			}
			got, err := decodeNamespaceRetiredMetadata(changed, expected)
			if err == nil || got != (namespacePinRetired{}) {
				t.Fatal("nonmatching marker returned value", got, err)
			}
		})
	}
	var o namespacePinOrigin
	if err := json.Unmarshal(originRaw, &o); err != nil {
		t.Fatal(err)
	}
	o.Directory.Inode++
	replacement, err := namespaceRetiredBindingFromMetadata(namespaceRetiredBindingMarshal(t, o), stateRaw, record, cid)
	if err != nil {
		t.Fatal("metadata-only helper must not invent a current mapping check", err)
	}
	if _, err := decodeNamespaceRetiredMetadata(raw, replacement); err == nil {
		t.Fatal("old marker adopted substituted origin bytes")
	}
	if _, err := namespaceRetiredBindingFromMetadata(originRaw, stateRaw, record, ""); err == nil {
		t.Fatal("missing recorded CID accepted")
	}
}
func TestNamespaceRetiredMarkerRejectsMalformedExpectedBindingAndClosedOldWrapper(t *testing.T) {
	files, record, cid, originRaw, stateRaw := namespaceRetiredBindingFixture(t, pinPlanned)
	expected, err := namespaceRetiredBindingFromMetadata(originRaw, stateRaw, record, cid)
	if err != nil {
		t.Fatal(err)
	}
	for _, which := range []string{"schema", "origin", "state", "record", "boot"} {
		value := expected
		switch which {
		case "schema":
			value.Schema = 3
		case "origin":
			value.OriginSHA256 = ""
		case "state":
			value.StateSHA256 = strings.ToUpper(value.StateSHA256)
		case "record":
			value.RecordSHA256 = "bad"
		case "boot":
			value.KernelBootID = "bad"
		}
		if got, err := decodeNamespaceRetiredMetadata(namespaceRetiredBindingMarshal(t, value), value); err == nil || got != (namespacePinRetired{}) {
			t.Fatal("invalid expected binding accepted", which, got, err)
		}
	}
	if err := files.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := pinRetirementBinding(files); err == nil || got != (namespacePinRetired{}) {
		t.Fatal("closed original effect wrapper accepted", got, err)
	}
	if got, err := pinRetirementBinding(nil); err == nil || got != (namespacePinRetired{}) {
		t.Fatal("nil original effect wrapper accepted", got, err)
	}
}
