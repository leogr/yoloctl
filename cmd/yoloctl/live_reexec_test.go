package main

import (
	"os"
	"testing"
)

// liveReexecEnv marks the children of an opt-in live test. Its real VM
// transport re-executes os.Executable() as `vm-internal …` (the Firecracker
// driver's helper and SSH bridge), which is this test binary; with the marker
// that invocation runs the CLI's own main instead of the tests. Only a live
// test sets it, and no ordinary test run passes `vm-internal`.
const liveReexecEnv = "YOLOCTL_TEST_LIVE_REEXEC"

func TestMain(m *testing.M) {
	if os.Getenv(liveReexecEnv) == "1" && len(os.Args) > 1 && os.Args[1] == "vm-internal" {
		main() // exits with the private command's status
		os.Exit(2)
	}
	os.Exit(m.Run())
}
