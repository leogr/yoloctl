package vmservice

import (
	"strings"
	"testing"
)

func TestSystemdProcessPropertiesRequireExactCompleteIdentity(t *testing.T) {
	const name = "yoloctl-openshell-port-web-8000-3000.service"
	const properties = "MainPID=421\nId=" + name + "\nActiveState=active\n"
	got, err := parseSystemdProcess(properties, name)
	if err != nil || got.Name != name || got.State != "active" || got.PID != 421 {
		t.Fatalf("exact properties: %+v, %v", got, err)
	}
	for _, raw := range []string{
		strings.Replace(properties, name, "yoloctl-port-other-8000-3000.service", 1),
		strings.Replace(properties, "421", "0", 1),
		strings.Replace(properties, "421", "1", 1),
		strings.Replace(properties, "421", "-2", 1),
		strings.Replace(properties, "421", "0421", 1),
		strings.Replace(properties, "active", "inactive", 1),
		strings.Replace(properties, "active", "surprise", 1),
		strings.Replace(properties, "ActiveState=active\n", "", 1),
		properties + "MainPID=422\n",
		properties + "ExecStart=/bin/sh\n",
		strings.Repeat("x", 4097),
	} {
		if _, err := parseSystemdProcess(raw, name); err == nil {
			t.Fatalf("accepted invalid main-process properties: %q", raw)
		}
	}
	stopped := "Id=" + name + "\nActiveState=inactive\nMainPID=0\n"
	if got, err := parseSystemdProcess(stopped, name); err != nil || got.PID != 0 || got.State != "inactive" {
		t.Fatalf("stopped observation: %+v, %v", got, err)
	}
}
