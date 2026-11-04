package container

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInspectTaskStateRetainsExplicitInactiveEvidenceWithoutInferringHistory(t *testing.T) {
	for _, status := range []string{"", "created", "exited", "stopped"} {
		for _, at := range []string{"", "0001-01-01T00:00:00Z", "2026-10-31T00:00:00Z"} {
			state, _ := json.Marshal(map[string]any{"Status": status, "Running": false, "Pid": 0, "StartedAt": at, "Paused": false, "Restarting": false, "ExitCode": 0, "Error": "", "FinishedAt": ""})
			raw := []byte(`[{"Id":"` + strings.Repeat("a", 64) + `","State":` + string(state) + `}]`)
			d, err := parseInspect(raw, "fixture")
			if err != nil || !d.InactiveTaskFieldsObserved || d.AmbiguousTaskStateObserved || d.EmptyTaskStateObserved != (status == "" && (at == "" || at == "0001-01-01T00:00:00Z")) || d.Status != status || d.StartedAt != at {
				t.Fatal("adapter lost explicit fields or invented history", status, at, d, err)
			}
			encoded, _ := json.Marshal(d)
			if strings.Contains(string(encoded), "TaskFieldsObserved") || strings.Contains(string(encoded), "EmptyTaskStateObserved") || strings.Contains(string(encoded), "AmbiguousTaskStateObserved") {
				t.Fatal("internal observation changed public JSON fields")
			}
		}
	}
}

func TestInspectTaskStateRefusesMissingNullAmbiguousAndUnknownInactiveShapes(t *testing.T) {
	for _, state := range []string{
		`null`, `{}`, `{"Status":"","Running":false,"StartedAt":""}`, `{"Status":"","Pid":0,"StartedAt":""}`,
		`{"Status":"","Running":false,"Pid":0}`, `{"Status":"","Running":false,"Pid":null,"StartedAt":""}`,
		`{"Status":null,"Running":false,"Pid":0,"StartedAt":""}`, `{"Status":"","Running":null,"Pid":0,"StartedAt":""}`,
		`{"Status":"","Running":false,"Pid":0,"StartedAt":null}`, `{"Status":"unknown","Running":false,"Pid":0,"StartedAt":""}`,
		`{"Status":"","Running":false,"Pid":0,"StartedAt":"bad-time"}`, `{"Status":"","Running":false,"Pid":0,"StartedAt":"","Paused":true}`,
		`{"Status":"","Running":false,"Pid":0,"StartedAt":"","Restarting":true}`, `{"Status":"","Running":false,"Pid":0,"StartedAt":"","future":true}`,
		`{"Status":"","Running":false,"Pid":0,"StartedAt":"","Running":false}`,
	} {
		raw := []byte(`[{"Id":"` + strings.Repeat("a", 64) + `","State":` + state + `}]`)
		d, err := parseInspect(raw, "fixture")
		if err != nil {
			continue
		} // existing typed-field decoder may also refuse
		if d.InactiveTaskFieldsObserved || d.EmptyTaskStateObserved {
			t.Fatal("incomplete/ambiguous fields became inactive evidence", state)
		}
	}
	for _, raw := range []string{
		`[{"Id":"a"}]`,
		`[{"Id":"a","State":{"Status":"","Running":false,"Pid":0,"StartedAt":""},"State":{"Status":"","Running":false,"Pid":0,"StartedAt":""}}]`,
	} {
		d, err := parseInspect([]byte(raw), "fixture")
		if err == nil && (d.InactiveTaskFieldsObserved || d.EmptyTaskStateObserved) {
			t.Fatal("missing/duplicate State became proof", raw)
		}
	}
}

func TestInspectTaskStateRefusesCaseFoldedStateAliasesInEitherOrder(t *testing.T) {
	inactive := `{"Status":"","Running":false,"Pid":0,"StartedAt":""}`
	running := `{"Status":"running","Running":true,"Pid":42,"StartedAt":"2026-10-31T00:00:00Z"}`
	paused := `{"Paused":true,"Restarting":true}`
	for _, alias := range []string{"state", "sTaTe", "ſtate"} {
		for _, members := range []string{
			`"` + alias + `":` + running + `,"State":` + inactive,
			`"State":` + inactive + `,"` + alias + `":` + running,
			`"` + alias + `":` + paused + `,"State":` + inactive,
			`"State":` + inactive + `,"` + alias + `":` + paused,
			`"` + alias + `":` + inactive,
		} {
			d, err := parseInspect([]byte(`[{"Id":"a",`+members+`}]`), "fixture")
			if err != nil {
				t.Fatal("fixture should retain legacy typed decoding", err)
			}
			if !d.AmbiguousTaskStateObserved || d.InactiveTaskFieldsObserved || d.EmptyTaskStateObserved {
				t.Fatal("aliased State acquired inactive proof", members, d)
			}
		}
	}
}
