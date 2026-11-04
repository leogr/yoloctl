package openshell

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestRuntimeBoundaryCarriesContainerEnvironmentOnlyToItsOwnRole(t *testing.T) {
	f := newMaterialFixture(t)
	material, err := readRuntimeMaterialAt(f.root, f.release, f.record.Launch.HelperLaunch(), f.owners)
	if err != nil {
		t.Fatal(err)
	}
	inherited := []string{
		"PATH=/home/sandbox/.npm-global/bin:/usr/local/bin:/usr/bin:/bin",
		"JAVA_HOME=/opt/custom-java", "LANG=en_US.UTF-8", "LITERAL=first", "LITERAL=last",
		"OPENSHELL_USER_ENVIRONMENT={\"API_KEY\":\"private-bootstrap-secret\"}",
		"OPENSHELL_ENDPOINT=https://untrusted.invalid", "OPENSHELL_LOG_LEVEL=trace",
	}
	material.boundaryEnv, err = captureBoundaryEnvironment(inherited)
	if err != nil {
		t.Fatal(err)
	}
	boundary, err := material.childPayload(runtimeBoundary)
	if err != nil {
		t.Fatal(err)
	}
	supervisor, err := material.childPayload(runtimeSupervisor)
	if err != nil {
		t.Fatal(err)
	}
	for _, payload := range []runtimeChildPayload{boundary, supervisor} {
		if err := payload.validate(f.release, f.record.Launch.HelperLaunch(), payload.Role); err != nil {
			t.Fatal(err)
		}
	}
	if len(supervisor.BoundaryEnv) != 0 || !slices.Contains(boundary.BoundaryEnv, inherited[0]) || !slices.Contains(boundary.BoundaryEnv, "LITERAL=last") {
		t.Fatal("container environment was lost or crossed into supervisor authority")
	}
	// This private role must not read its re-exec process's ambient variables.
	t.Setenv("UNRELATED_AMBIENT", "must-not-enter-boundary")
	environment, err := runtimeBoundaryEnvironment(boundary.BoundaryEnv)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{inherited[0], "JAVA_HOME=/opt/custom-java", "LANG=en_US.UTF-8", "LITERAL=last", "HOME=/home/sandbox", "OPENSHELL_LOG_LEVEL=info"} {
		if !slices.Contains(environment, want) {
			t.Fatal("container/default environment entry missing")
		}
	}
	for _, forbidden := range []string{"private-bootstrap-secret", "untrusted.invalid", "trace", "must-not-enter-boundary", "LITERAL=first"} {
		if strings.Contains(strings.Join(environment, "\n"), forbidden) || strings.Contains(strings.Join(SupervisorEnvironment(), "\n"), forbidden) {
			t.Fatal("environment control/private channel crossed its role boundary")
		}
	}
	// Caller mutation cannot change the already selected child's private copy.
	material.boundaryEnv[0] = "CHANGED=after-copy"
	if slices.Contains(boundary.BoundaryEnv, "CHANGED=after-copy") {
		t.Fatal("private child environment aliases its source slice")
	}
	for _, value := range []any{material, boundary, supervisor} {
		if strings.Contains(fmt.Sprintf("%v %#v", value, value), "custom-java") {
			t.Fatal("diagnostic formatting exposed workload environment")
		}
	}
}

func TestRuntimeBoundaryEnvironmentRefusesControlAndRoleInjection(t *testing.T) {
	f := newMaterialFixture(t)
	material, err := readRuntimeMaterialAt(f.root, f.release, f.record.Launch.HelperLaunch(), f.owners)
	if err != nil {
		t.Fatal(err)
	}
	for _, environment := range [][]string{
		{"OPENSHELL_ENDPOINT=https://untrusted.invalid"},
		{"PATH=/first", "PATH=/second"},
		{"Z=last", "A=first"},
		{"=empty-key"}, {"missing-equals"}, {"KEY=bad\x00value"},
		{"KEY=" + strings.Repeat("x", maxRuntimeChildBytes)},
	} {
		payload, err := material.childPayload(runtimeBoundary)
		if err != nil {
			t.Fatal(err)
		}
		payload.BoundaryEnv = environment
		if err := payload.validate(f.release, f.record.Launch.HelperLaunch(), runtimeBoundary); err == nil {
			t.Fatal("malformed/control environment accepted in private payload")
		}
		if _, err := runtimeBoundaryEnvironment(environment); err == nil {
			t.Fatal("final environment accepted malformed/control private payload")
		}
	}
	payload, err := material.childPayload(runtimeSupervisor)
	if err != nil {
		t.Fatal(err)
	}
	payload.BoundaryEnv = []string{"PATH=/image-only"}
	if err := payload.validate(f.release, f.record.Launch.HelperLaunch(), runtimeSupervisor); err == nil {
		t.Fatal("supervisor accepted workload environment")
	}
}

func TestRuntimeBoundaryEnvironmentPreservesExplicitEmptyValuesAndRejectsMalformedCapture(t *testing.T) {
	got, err := captureBoundaryEnvironment([]string{"PATH=", "VALUE=a=b", "EMPTY="})
	if err != nil {
		t.Fatal(err)
	}
	environment, err := runtimeBoundaryEnvironment(got)
	if err != nil || !slices.Contains(environment, "PATH=") || !slices.Contains(environment, "VALUE=a=b") || !slices.Contains(environment, "EMPTY=") {
		t.Fatal("explicit empty/value with equals was replaced", err)
	}
	for _, malformed := range [][]string{{"missing-equals"}, {"=empty-key"}, {"KEY=bad\x00value"}, {"KEY=" + strings.Repeat("x", maxRuntimeChildBytes)}} {
		if _, err := captureBoundaryEnvironment(malformed); err == nil {
			t.Fatal("invalid/unbounded inherited environment was accepted")
		}
	}
}
