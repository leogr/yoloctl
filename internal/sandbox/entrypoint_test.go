package sandbox

import (
	"slices"
	"testing"
)

// EntrypointArgs appends "--mode process" iff unconfinedNetwork is true, keeps
// the policy/workdir/ssh flags in BOTH modes (openshell requires a policy in
// every mode — it still drives the filesystem/landlock/process blocks), and
// preserves the post-"--" child argv.
func TestEntrypointArgs_Mode(t *testing.T) {
	// Mandatory in every mode: the policy/workdir/ssh flags.
	mandatory := []string{"--policy-rules", "--policy-data", "--workdir", "--ssh-socket-path"}

	confined := EntrypointArgs("/workdir", false, "sleep", "infinity")
	if slices.Contains(confined, "--mode") {
		t.Errorf("confined args must NOT carry --mode (default is network,process): %v", confined)
	}
	for _, want := range mandatory {
		if !slices.Contains(confined, want) {
			t.Errorf("confined args missing mandatory flag %s: %v", want, confined)
		}
	}

	unconfined := EntrypointArgs("/workdir", true, "sleep", "infinity")
	for _, want := range mandatory {
		if !slices.Contains(unconfined, want) {
			t.Errorf("unconfined args must STILL carry %s (fs/landlock stay enforced): %v", want, unconfined)
		}
	}
	mode := slices.Index(unconfined, "--mode")
	if mode == -1 || mode+1 >= len(unconfined) || unconfined[mode+1] != "process" {
		t.Fatalf("unconfined args must carry `--mode process`, got %v", unconfined)
	}
	// --mode is a sandbox flag, so it must come BEFORE the `--` child separator.
	sep := slices.Index(unconfined, "--")
	if sep == -1 {
		t.Fatalf("expected a `--` child separator, got %v", unconfined)
	}
	if mode > sep {
		t.Errorf("--mode must precede the `--` child separator, got %v", unconfined)
	}
	// The supervised child argv survives after `--` in both modes.
	if !slices.Equal(unconfined[sep+1:], []string{"sleep", "infinity"}) {
		t.Errorf("child argv not preserved after --: %v", unconfined)
	}
}
