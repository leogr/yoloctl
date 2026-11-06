package container

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"text/template"
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

func TestAuditProjectionExecutesTypedDockercompatFields(t *testing.T) {
	// nerdctl's dockercompat entry exposes ID as a Go field. Its JSON tag Id
	// does not make .Id valid during the formatter's first typed execution.
	// Do not preassemble JSON: that would hide the live formatter failure.
	fixture := struct {
		ID     string `json:"Id"`
		Config struct {
			Labels               map[string]string
			Env, Cmd, Entrypoint []string
		}
		HostConfig struct{ Privileged bool }
	}{ID: strings.Repeat("a", 64)}
	fixture.Config.Labels = map[string]string{
		"yoloctl.audit.schema": "1", "yoloctl.kind": "agentbox", "yoloctl.agentbox": "worker", "yoloctl.space": "work",
		"yoloctl.audit.account": "none", "yoloctl.agent": "none", "yoloctl.audit.runtime_mode": "confined", "yoloctl.audit.network_unconfined": "false",
		"unselected": "SECRET_LABEL",
	}
	fixture.Config.Env = []string{"SECRET_ENV"}
	fixture.Config.Cmd = []string{"SECRET_ARGV"}
	fixture.Config.Entrypoint = []string{"SECRET_ENTRYPOINT"}
	tmpl, err := template.New("inspect").Funcs(template.FuncMap{"json": func(value any) (string, error) { raw, err := json.Marshal(value); return string(raw), err }}).Parse(auditProjection)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, fixture); err != nil {
		t.Fatal("production template failed against typed dockercompat entry", err)
	}
	if bytes.Contains(out.Bytes(), []byte("SECRET")) {
		t.Fatal("projection exposed unselected data")
	}
	rows, err := parseAuditLabels(out.Bytes(), []string{fixture.ID})
	privileged := false
	want := AuditLabelObservation{ID: fixture.ID, Schema: "1", Kind: "agentbox", Box: "worker", Space: "work", Account: "none", Agent: "none", RuntimeMode: "confined", Network: "false", Privileged: &privileged}
	if err != nil || !reflect.DeepEqual(rows, []AuditLabelObservation{want}) {
		t.Fatal("typed projection lost selected metadata", rows, err)
	}
	// Pinned nerdctl can exit zero after appending a raw fallback to this
	// already-written prefix. Successful transport must not admit that row.
	malformed := append([]byte(`{"id":`), out.Bytes()...)
	if _, err := parseAuditLabels(malformed, []string{fixture.ID}); err == nil {
		t.Fatal("accepted malformed fallback despite successful transport")
	}
}
