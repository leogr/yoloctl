package openshell

import (
	"reflect"
	"strings"
	"testing"
)

func TestHelperLaunchIsInspectableAndRejectsUnreviewedOverrides(t *testing.T) {
	launch := HelperLaunch{HelperProtocol, "/workdir/home/me/project", strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 32)}
	args, err := launch.Args()
	if err != nil {
		t.Fatal(err)
	}
	if args[0] != HelperBinary || args[1] != HelperCommand {
		t.Fatal("helper binary/command mismatch")
	}
	parsed, err := ParseHelperLaunch(args[2:])
	if err != nil || !reflect.DeepEqual(parsed, launch) {
		t.Fatal(parsed, err)
	}
	for _, bad := range [][]string{
		args[3:], append(append([]string{}, args[2:]...), "--workdir", "/elsewhere"),
		append(append([]string{}, args[2:]...), "--workspace-read-only", "false"),
		append(append([]string{}, args[2:]...), "--command", "/bin/sh"),
		append(append([]string{}, args[2:]...), "--"),
		append([]string{"boundary"}, args[3:]...),
	} {
		if _, err := ParseHelperLaunch(bad); err == nil {
			t.Fatalf("unsafe helper launch accepted: %q", bad)
		}
	}
	launch.Protocol = "1"
	args, err = launch.Args()
	if err != nil {
		t.Fatal(err)
	}
	if parsed, err = ParseHelperLaunch(args[2:]); err != nil || parsed.Protocol != "1" {
		t.Fatal("historical launch cannot be inspected", err)
	}
}

func TestHelperChildrenReceiveOnlyRoleEnvironment(t *testing.T) {
	t.Setenv("OPENSHELL_ENDPOINT", "https://untrusted.invalid")
	t.Setenv("OPENSHELL_USER_ENVIRONMENT", `{"SECRET":"private-value"}`)
	t.Setenv("LD_PRELOAD", "/untrusted.so")
	t.Setenv("HTTP_PROXY", "http://untrusted.invalid")
	for _, env := range [][]string{SupervisorEnvironment(), BoundaryEnvironment()} {
		joined := strings.Join(env, "\n")
		for _, forbidden := range []string{"untrusted", "private-value", "OPENSHELL_USER_ENVIRONMENT", "LD_PRELOAD", "HTTP_PROXY"} {
			if strings.Contains(joined, forbidden) {
				t.Fatal("role inherited workload/root environment")
			}
		}
	}
	if !strings.Contains(strings.Join(SupervisorEnvironment(), "\n"), "OPENSHELL_ADMITTED_ISOLATION_BACKEND=openshell-sandbox") {
		t.Fatal("missing explicit backend admission")
	}
}
