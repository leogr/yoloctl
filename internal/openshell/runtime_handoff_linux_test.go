package openshell

import (
	"strings"
	"testing"
)

func TestRuntimeExecRefusesAnOrdinaryCallerBeforePrivateFDUse(t *testing.T) {
	f := newMaterialFixture(t)
	for _, role := range []runtimeChildRole{runtimeSupervisor, runtimeBoundary} {
		if err := execRuntimeChild(role, f.record.Launch.HelperLaunch(), f.release); err == nil {
			t.Fatal("ordinary process entered a private runtime exec path")
		}
	}
}

func TestRuntimeChildRequiresCompleteFixedParentCommandAndRootIdentity(t *testing.T) {
	f := newMaterialFixture(t)
	launch := f.record.Launch.HelperLaunch()
	args, err := launch.Args()
	if err != nil {
		t.Fatal(err)
	}
	command := strings.Join(args, "\x00") + "\x00"
	if err := matchRuntimeParentCommand([]byte(command), launch); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"", strings.TrimSuffix(command, "\x00"), command + "extra\x00", strings.Replace(command, "runtime\x00", "boundary\x00", 1), strings.Repeat("x", (64<<10)+1)} {
		if err := matchRuntimeParentCommand([]byte(raw), launch); err == nil {
			t.Fatal("incorrect runtime parent launch accepted")
		}
	}
	status := "Name:\tyoloctl\nPPid:\t0\nUid:\t0 0 0 0\nGid:\t0 0 0 0\nNSpid:\t71 1\n"
	if err := matchRuntimeParentStatus([]byte(status)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"", strings.Replace(status, "Uid:\t0 0 0 0", "Uid:\t1000 0 0 0", 1), strings.Replace(status, "Gid:\t0 0 0 0", "Gid:\t0 0 1000 0", 1), strings.Replace(status, "PPid:\t0", "PPid:\t7", 1), strings.Replace(status, "71 1", "71 2", 1), status + "Uid:\t0 0 0 0\n", strings.Replace(status, "NSpid:", "Other:", 1)} {
		if err := matchRuntimeParentStatus([]byte(raw)); err == nil {
			t.Fatal("incorrect runtime parent process identity accepted")
		}
	}
}
