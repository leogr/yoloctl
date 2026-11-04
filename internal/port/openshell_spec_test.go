package port

import (
	"slices"
	"strings"
	"testing"
)

func forwardBindingFixture() ForwardBinding {
	return ForwardBinding{ContainerID: strings.Repeat("1", 64), Incarnation: strings.Repeat("2", 32), Qualification: strings.Repeat("3", 64), HelperSHA256: strings.Repeat("4", 64),
		Generation: "g" + strings.Repeat("5", 48), RecordSHA256: strings.Repeat("6", 64), Attempt: strings.Repeat("7", 32)}
}

func TestOpenShellForwardDescriptorNamesAndFixedArguments(t *testing.T) {
	spec := Spec{Agentbox: "web-server", HostPort: 8080, ContainerPort: 80}
	name, err := OpenShellUnitName(spec)
	if err != nil {
		t.Fatal(err)
	}
	if name == UnitName(spec) {
		t.Fatal("new SSH unit is indistinguishable from legacy DNAT")
	}
	parsed, ok := ParseOpenShellUnitName(name)
	if !ok || parsed != spec {
		t.Fatal("new transport name lost declared target", parsed, ok)
	}
	binding := forwardBindingFixture()
	args, err := ForwardWorkerArgs(spec, binding)
	if err != nil {
		t.Fatal(err)
	}
	if args[0] != ForwardWorkerCommand {
		t.Fatal("descriptor selected another executable role")
	}
	got, actual, err := ParseForwardWorker(args[1:])
	if err != nil || got != spec || actual != binding {
		t.Fatal("fixed descriptor did not roundtrip", got, actual, err)
	}
	for _, unsafe := range []string{"--address", "--command", "--socket", "--key", "--pid", "--netns", "/bin/sh"} {
		if slices.Contains(args, unsafe) {
			t.Fatal("caller-selected execution/network authority entered descriptor", unsafe)
		}
	}
}

func TestOpenShellForwardDescriptorRefusesForeignAndMutableTargets(t *testing.T) {
	spec := Spec{Agentbox: "web", HostPort: 8080, ContainerPort: 80}
	binding := forwardBindingFixture()
	args, err := ForwardWorkerArgs(spec, binding)
	if err != nil {
		t.Fatal(err)
	}
	for _, extra := range [][]string{{"--address", "192.168.1.1"}, {"--command", "sh"}, {"--pid", "42"}, {"--target-port", "81"}, {"--host-port", "8080"}, {"--unknown", "x"}} {
		if _, _, err := ParseForwardWorker(append(slices.Clone(args[1:]), extra...)); err == nil {
			t.Fatal("foreign/duplicate worker target accepted", extra[0])
		}
	}
	for _, bad := range []string{"0", "65536", "+80", "080", "127.0.0.1:80"} {
		mutated := slices.Clone(args[1:])
		for i := 0; i < len(mutated); i += 2 {
			if mutated[i] == "--target-port" {
				mutated[i+1] = bad
			}
		}
		if _, _, err := ParseForwardWorker(mutated); err == nil {
			t.Fatal("noncanonical/flexible target accepted", bad)
		}
	}
	if _, _, err := ParseForwardWorker(args[1 : len(args)-2]); err == nil {
		t.Fatal("missing owned attempt accepted")
	}
	spec.Direct = true
	if _, err := ForwardWorkerArgs(spec, binding); err == nil {
		t.Fatal("privileged route entered ordinary SSH worker")
	}
	for _, name := range []string{UnitName(Spec{Agentbox: "web", HostPort: 8080, ContainerPort: 80}), "yoloctl-openshell-port-web-8080-0.service", "yoloctl-openshell-port-web-08080-80.service", "yoloctl-openshell-port-web-8080-80.service.extra", "yoloctl-openshell-port-../web-8080-80.service"} {
		if _, ok := ParseOpenShellUnitName(name); ok {
			t.Fatal("foreign/noncanonical service name accepted", name)
		}
	}
}
