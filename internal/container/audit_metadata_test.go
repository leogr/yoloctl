package container

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAuditProjectionRefusesIncompleteOrMismatchedIDs(t *testing.T) {
	id := strings.Repeat("a", 64)
	row := AuditLabelObservation{ID: id, Schema: "1", Kind: "agentbox", Box: "worker", Space: "work", Account: "none", Agent: "none", RuntimeMode: "confined", Network: "false"}
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	result, err := parseAuditLabels(raw, []string{id})
	if err != nil || len(result) != 1 || result[0].Box != "worker" {
		t.Fatal(result, err)
	}
	for _, bad := range [][]byte{nil, append(append([]byte{}, raw...), raw...), []byte(`{"id":"missing"}`), append(raw, []byte("garbage")...), []byte(`{"id":"` + id + `","env":["SECRET"]}`)} {
		if _, err := parseAuditLabels(bad, []string{id}); err == nil {
			t.Fatal("accepted incomplete/foreign projection", string(bad))
		}
	}
	for _, forbidden := range []string{".Config.Env", ".Config.Cmd", ".Config.Entrypoint", "json .Config.Labels"} {
		if strings.Contains(auditProjection, forbidden) {
			t.Fatal("projection reads unselected data", forbidden)
		}
	}
	for _, selected := range []string{"yoloctl.audit.schema", "yoloctl.audit.account", "yoloctl.audit.runtime_mode", "yoloctl.audit.network_unconfined"} {
		if !strings.Contains(auditProjection, selected) {
			t.Fatal("missing observation label", selected)
		}
	}
}
