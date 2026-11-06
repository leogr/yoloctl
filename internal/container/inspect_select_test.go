package container

import (
	"strings"
	"testing"
)

// nerdctlEntry is one element of a nerdctl 2.3.5 `inspect` array (dockercompat
// shape, trimmed): name without a leading "/", the name label, the OCI argv
// as Path/Args (empty for a spec without process args) and the IP address.
func nerdctlEntry(id, name, path, ip string) string {
	label := ""
	if name != "" {
		label = `"nerdctl/name":"` + name + `",`
	}
	args := `[]`
	if path != "" {
		args = `["--workdir","/workdir"]`
	}
	return `{"Id":"` + id + `","Created":"2026-11-06T12:00:00Z","Path":"` + path + `","Args":` + args +
		`,"State":{"Status":"exited","Running":false,"Pid":0,"StartedAt":"2026-11-06T12:00:01Z"},"Image":"sha256:aa","Name":"` + name +
		`","Config":{"Labels":{` + label + `"yoloctl.agentbox":"x"}},"Mounts":[],"NetworkSettings":{"IPAddress":"` + ip + `"}}`
}

func inspectArray(entries ...string) []byte { return []byte("[" + strings.Join(entries, ",") + "]") }

// The triage case: `nerdctl inspect c1` also returns a container whose ID
// starts with "c1" and sorts first; only the box named c1 is selected.
func TestInspectSelectsTheExactlyNamedContainer(t *testing.T) {
	prefix := "c10b" + strings.Repeat("0", 60) // no name, no process args
	own := "f3" + strings.Repeat("1", 62)
	raw := inspectArray(nerdctlEntry(prefix, "", "", ""), nerdctlEntry(own, "c1", "/usr/local/bin/yoloctl-entrypoint", "10.4.0.7"))
	d, err := parseInspect(raw, "c1")
	if err != nil || d.ID != own || d.Name != "c1" || len(d.Command) == 0 {
		t.Fatalf("selected %+v, %v", d, err)
	}
	ip, err := parseContainerIP(raw, "c1")
	if err != nil || ip != "10.4.0.7" {
		t.Fatalf("IP of the named container: %q, %v", ip, err)
	}

	// The box is gone and only a prefix match remains: no such container,
	// which callers already treat as gone (never another container's state).
	only := inspectArray(nerdctlEntry(prefix, "", "", ""))
	if _, err := parseInspect(only, "c1"); err == nil || !strings.Contains(err.Error(), "no such container") {
		t.Fatalf("prefix-only match: %v", err)
	}
	if _, err := parseContainerIP(only, "c1"); err == nil || !strings.Contains(err.Error(), "no such container") {
		t.Fatalf("prefix-only IP match: %v", err)
	}
}

// Another box whose ID starts with the requested name is never returned in
// its place, whatever the order.
func TestInspectNeverReturnsAnotherBoxByIDPrefix(t *testing.T) {
	other := "c1" + strings.Repeat("a", 62)
	own := "c1" + strings.Repeat("b", 62)
	for _, raw := range [][]byte{
		inspectArray(nerdctlEntry(other, "c1x", "/usr/local/bin/yoloctl-entrypoint", ""), nerdctlEntry(own, "c1", "/usr/local/bin/yoloctl-entrypoint", "")),
		inspectArray(nerdctlEntry(own, "c1", "/usr/local/bin/yoloctl-entrypoint", ""), nerdctlEntry(other, "c1x", "/usr/local/bin/yoloctl-entrypoint", "")),
	} {
		d, err := parseInspect(raw, "c1")
		if err != nil || d.ID != own {
			t.Fatalf("selected %+v, %v", d, err)
		}
	}
	// Two containers recorded under the same name are ambiguous.
	twice := inspectArray(nerdctlEntry(other, "c1", "/a", ""), nerdctlEntry(own, "c1", "/b", ""))
	if _, err := parseInspect(twice, "c1"); err == nil || !strings.Contains(err.Error(), "more than one") {
		t.Fatalf("two entries named c1: %v", err)
	}
}

// Lifecycle callers pass a fixed full container ID: it selects that entry
// even though its name differs, and a prefix of it selects nothing.
func TestInspectByFullContainerID(t *testing.T) {
	id := "d4" + strings.Repeat("e", 62)
	raw := inspectArray(nerdctlEntry(id, "coder", "/usr/local/bin/yoloctl-entrypoint", ""))
	d, err := parseInspect(raw, id)
	if err != nil || d.ID != id || d.Name != "coder" {
		t.Fatalf("full ID: %+v, %v", d, err)
	}
	if _, err := parseInspect(raw, id[:12]); err == nil {
		t.Fatal("a short ID prefix selected a container")
	}
}

// The name label alone identifies a container, and a stopped container's
// empty IP is still a success.
func TestInspectNameLabelAndEmptyIP(t *testing.T) {
	id := "0a" + strings.Repeat("2", 62)
	raw := []byte(`[{"Id":"` + id + `","Name":"","Path":"/x","Args":[],"State":{"Status":"exited"},"Config":{"Labels":{"nerdctl/name":"web"}},"NetworkSettings":{"IPAddress":""}}]`)
	if d, err := parseInspect(raw, "web"); err != nil || d.ID != id {
		t.Fatalf("label match: %+v, %v", d, err)
	}
	if ip, err := parseContainerIP(raw, "web"); err != nil || ip != "" {
		t.Fatalf("empty IP: %q, %v", ip, err)
	}
}
