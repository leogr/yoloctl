package openshell

import "testing"

func TestRuntimeResolverAliasesCannotHideProjectOrPrivateState(t *testing.T) {
	for _, target := range []string{"/etc/resolv.conf", "/run/systemd/resolve/resolv.conf", "/usr/etc/resolv.conf"} {
		if err := validateRuntimeResolverTarget(target, "/workdir/project"); err != nil {
			t.Fatal(target, err)
		}
	}
	for _, target := range []string{"/", "relative", "/etc/../etc/resolv.conf", "/etc/resolv.conf (deleted)\n", "/workdir/project", "/workdir/project/resolv.conf", "/home/sandbox/.codex/resolv.conf", "/.openshell/native/current.json", "/proc/1/root/etc/resolv.conf", "/sys/resolv.conf", "/dev/null", "/run/yoloctl/peer/ssh.sock"} {
		if err := validateRuntimeResolverTarget(target, "/workdir/project"); err == nil {
			t.Fatal("unsafe resolver alias accepted", target)
		}
	}
}
