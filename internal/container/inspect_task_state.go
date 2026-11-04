package container

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"time"
)

func (e *inspectEntry) UnmarshalJSON(raw []byte) error {
	type fields inspectEntry
	var decoded fields
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*e = inspectEntry(decoded)
	object, ok := uniqueInspectionObject(raw)
	e.ambiguousTaskStateObserved = !ok || !canonicalInspectionStateOnly(object)
	if !e.ambiguousTaskStateObserved {
		e.inactiveTaskFieldsObserved, e.emptyTaskStateObserved = observedInactiveTaskState(object["State"])
	}
	return nil
}

// encoding/json also matches case-folded field names. Withhold evidence for
// any alias, even when a later canonical key leaves the decoded task looking
// inactive, or an alias last leaves it looking like the selected live process.
// EqualFold uses the same Unicode folding (including long-s) as that decoder.
func canonicalInspectionStateOnly(object map[string]json.RawMessage) bool {
	for name := range object {
		if name != "State" && strings.EqualFold(name, "State") {
			return false
		}
	}
	_, exists := object["State"]
	return exists
}

// This strengthens only the new task-state evidence; existing inspection
// field decoding is unchanged. Missing/null/duplicate/unknown state cannot
// masquerade as the pinned adapter's explicit zero process fields.
func uniqueInspectionObject(raw []byte) (map[string]json.RawMessage, bool) {
	d := json.NewDecoder(bytes.NewReader(raw))
	first, err := d.Token()
	if err != nil || first != json.Delim('{') {
		return nil, false
	}
	result := map[string]json.RawMessage{}
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return nil, false
		}
		name, ok := key.(string)
		if !ok {
			return nil, false
		}
		if _, exists := result[name]; exists {
			return nil, false
		}
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return nil, false
		}
		result[name] = value
		if len(result) > 4096 {
			return nil, false
		}
	}
	last, err := d.Token()
	if err != nil || last != json.Delim('}') {
		return nil, false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, false
	}
	return result, true
}

func observedInactiveTaskState(raw []byte) (inactive, empty bool) {
	state, ok := uniqueInspectionObject(raw)
	if !ok {
		return false, false
	}
	for name := range state {
		switch name {
		case "Status", "Running", "Paused", "Restarting", "Pid", "ExitCode", "Error", "StartedAt", "FinishedAt", "Health":
		default:
			return false, false
		}
	}
	statusRaw := bytes.TrimSpace(state["Status"])
	if len(statusRaw) == 0 || statusRaw[0] != '"' {
		return false, false
	}
	var status string
	if json.Unmarshal(statusRaw, &status) != nil {
		return false, false
	}
	switch status {
	case "", "created", "exited", "stopped":
	default:
		return false, false
	}
	if !bytes.Equal(bytes.TrimSpace(state["Running"]), []byte("false")) || !bytes.Equal(bytes.TrimSpace(state["Pid"]), []byte("0")) {
		return false, false
	}
	started := bytes.TrimSpace(state["StartedAt"])
	if len(started) == 0 || started[0] != '"' {
		return false, false
	}
	var at string
	if json.Unmarshal(started, &at) != nil {
		return false, false
	}
	zeroHistory := at == ""
	if at != "" {
		timestamp, err := time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return false, false
		}
		zeroHistory = timestamp.IsZero()
	}
	for _, name := range []string{"Paused", "Restarting"} {
		if value, exists := state[name]; exists && !bytes.Equal(bytes.TrimSpace(value), []byte("false")) {
			return false, false
		}
	}
	if status != "" || !zeroHistory {
		return true, false
	}
	if value, exists := state["ExitCode"]; exists && !bytes.Equal(bytes.TrimSpace(value), []byte("0")) {
		return true, false
	}
	if value, exists := state["FinishedAt"]; exists && !bytes.Equal(bytes.TrimSpace(value), []byte(`""`)) {
		return true, false
	}
	return true, true
}
