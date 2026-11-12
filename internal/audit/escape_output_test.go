package audit

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEscapeOutputRetainsOnlySelectedUIDAndNetworkContext(t *testing.T) {
	fields := map[string]any{
		"evt.arg.uid": 0, "evt.arg.ruid": 0, "evt.arg.euid": 0, "evt.arg.suid": 0,
		"user.uid": 1000, "proc.pexepath": "/usr/bin/sh", "proc.exepath": "/usr/bin/probe",
		"fd.cip": "10.200.0.2", "fd.rip": "192.0.2.1", "fd.rport": 443,
		"evt.res": "SUCCESS", "evt.args": "private-arguments",
		"proc.cmdline": "private-arguments", "proc.env": "private-environment",
		"fd.lport": 12345, "evt.arg.unreviewed": 12,
	}
	for _, rule := range []string{"Yolo UID set to root", "Yolo connect outside inner netns"} {
		raw, err := json.Marshal(map[string]any{"time": "2026-11-12T00:00:00Z", "rule": rule, "priority": "Critical", "output_fields": fields})
		if err != nil {
			t.Fatal(err)
		}
		event, err := ParseEvent(raw)
		if err != nil {
			t.Fatal(err)
		}
		for key, want := range map[string]string{
			"evt.arg.uid": "0", "evt.arg.ruid": "0", "evt.arg.euid": "0", "evt.arg.suid": "0",
			"user.uid": "1000", "fd.cip": "10.200.0.2", "fd.rport": "443", "proc.pexepath": "/usr/bin/sh",
		} {
			if event.OutputFields[key] != want {
				t.Fatal(key, event.OutputFields)
			}
		}
		stored, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"private-", "evt.args", "proc.cmdline", "proc.env", "fd.lport", "evt.arg.unreviewed"} {
			if strings.Contains(string(stored), forbidden) {
				t.Fatal("unreviewed data persisted", forbidden, string(stored))
			}
		}
		if rule == "Yolo UID set to root" {
			for _, want := range []string{"requested_uid=0", "requested_ruid=0", "requested_euid=0", "requested_suid=0", "parent=/usr/bin/sh"} {
				if !strings.Contains(event.Output, want) {
					t.Fatal(want, event.Output)
				}
			}
		} else if !strings.Contains(event.Output, "client=10.200.0.2 remote=192.0.2.1 port=443") {
			t.Fatal(event.Output)
		}
	}
}

func TestEscapeNumericFieldsRemainBoundedAndDoNotCoerceOtherTypes(t *testing.T) {
	for _, key := range []string{"user.uid", "evt.arg.uid", "evt.arg.ruid", "evt.arg.euid", "evt.arg.suid", "fd.rport"} {
		for _, value := range []any{nil, true, []int{0}, map[string]int{"uid": 0}, -1, 0.5, 4294967296.0} {
			raw, err := json.Marshal(map[string]any{"time": "2026-11-12T00:00:00Z", "rule": "Yolo UID set to root", "priority": "Critical", "output_fields": map[string]any{key: value}})
			if err != nil {
				t.Fatal(err)
			}
			event, err := ParseEvent(raw)
			if err != nil || event.OutputFields[key] != "<NA>" {
				t.Fatal(key, value, event.OutputFields, err)
			}
		}
	}
	raw := []byte(`{"time":"2026-11-12T00:00:00Z","rule":"Yolo UID set to root","priority":"Critical","output_fields":{"evt.arg.uid":4294967295,"fd.rport":65535,"proc.pexepath":0,"fd.cip":0}}`)
	event, err := ParseEvent(raw)
	if err != nil || event.OutputFields["evt.arg.uid"] != "4294967295" || event.OutputFields["fd.rport"] != "65535" || event.OutputFields["proc.pexepath"] != "<NA>" || event.OutputFields["fd.cip"] != "<NA>" {
		t.Fatal(event.OutputFields, err)
	}
	raw = []byte(`{"time":"2026-11-12T00:00:00Z","rule":"Yolo UID set to root","priority":"Critical","output_fields":{"evt.arg.uid":"0","fd.rport":"443","evt.arg.euid":0,"fd.cip":"<NA>"}}`)
	event, err = ParseEvent(raw)
	if err != nil || event.OutputFields["evt.arg.uid"] != "0" || event.OutputFields["fd.rport"] != "443" || event.OutputFields["evt.arg.euid"] != "0" || !strings.Contains(event.Output, "requested_ruid=<NA>") {
		t.Fatal(event, err)
	}
}
