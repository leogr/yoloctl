package agentbox

import (
	"errors"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentsession/observe"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/sandbox"
)

// stubObservationRoot records root mounts and removals instead of mounting.
func stubObservationRoot(t *testing.T, fail error) (ensured, removed *[]string) {
	t.Helper()
	ensured, removed = &[]string{}, &[]string{}
	saved, savedRemove := ensureObservationRoot, removeObservationRoot
	t.Cleanup(func() { ensureObservationRoot, removeObservationRoot = saved, savedRemove })
	ensureObservationRoot = func(dir string) error { *ensured = append(*ensured, dir); return fail }
	removeObservationRoot = func(dir string) error { *removed = append(*removed, dir); return nil }
	return ensured, removed
}

func observedConfig() *Config {
	c := validConfig()
	c.Agent, c.Account = "claude", "default"
	return c
}

func TestObservationRunOptionsBindTheMountedRootForAgentBoxes(t *testing.T) {
	ensured, _ := stubObservationRoot(t, nil)
	c := observedConfig()
	opts := container.RunOptions{Mounts: buildMounts(c)}
	if err := addObservationRunOptions(c, &opts); err != nil {
		t.Fatal(err)
	}
	dir, _ := layout.AgentObserveDir(c.Name)
	last := opts.Mounts[len(opts.Mounts)-1]
	if len(*ensured) != 1 || (*ensured)[0] != dir || last != (container.Mount{Source: dir, Destination: observe.ContainerDir}) {
		t.Fatalf("the root is mounted before its read-write bind: %v %+v", *ensured, last)
	}
	if strings.HasPrefix(dir, sandbox.SocketDirHost(c.Name)+"/") {
		t.Fatal("the root must not sit inside the socket directory that is already bound")
	}

	agentless := validConfig()
	agentless.Agent = "none"
	opts = container.RunOptions{Mounts: buildMounts(agentless)}
	if err := addObservationRunOptions(agentless, &opts); err != nil || len(*ensured) != 1 || len(opts.Mounts) != len(buildMounts(agentless)) {
		t.Fatalf("agentless boxes get no observation root: %v %v", err, *ensured)
	}

	for _, target := range []string{"/run/yoloctl", observe.ContainerDir, observe.ContainerDir + "/gen-1"} {
		opts = container.RunOptions{Mounts: append(buildMounts(c), container.Mount{Source: "/host/x", Destination: target})}
		if err := addObservationRunOptions(c, &opts); err == nil {
			t.Errorf("an ordinary mount at %s overlaps the observation directory", target)
		}
	}
}

func TestObservationRunOptionsFailWhenTheRootCannotBeVerified(t *testing.T) {
	stubObservationRoot(t, observe.ErrUnavailable)
	c := observedConfig()
	opts := container.RunOptions{Mounts: buildMounts(c)}
	if err := addObservationRunOptions(c, &opts); !errors.Is(err, observe.ErrUnavailable) {
		t.Fatalf("an unverified root is never bound: %v", err)
	}
}

func TestObservationRuntimeFollowsTheContainersBind(t *testing.T) {
	ensured, removed := stubObservationRoot(t, nil)
	c := observedConfig()
	dir, _ := layout.AgentObserveDir(c.Name)
	old := &container.Details{Mounts: []container.DetailMount{{Source: sandbox.SocketDirHost(c.Name), Destination: sandbox.SocketDirContainer}}}
	if err := PrepareObservationRuntime(c, old); err != nil || len(*ensured) != 0 {
		t.Fatalf("a container without the bind is left alone: %v %v", err, *ensured)
	}
	bound := &container.Details{Mounts: append(old.Mounts, container.DetailMount{Source: dir, Destination: observe.ContainerDir})}
	if err := PrepareObservationRuntime(c, bound); err != nil || len(*ensured) != 1 || (*ensured)[0] != dir {
		t.Fatalf("a bound container gets its root back before start: %v %v", err, *ensured)
	}
	for _, m := range []container.DetailMount{
		{Source: "/run/elsewhere", Destination: observe.ContainerDir},
		{Source: dir, Destination: observe.ContainerDir, ReadOnly: true},
	} {
		foreign := &container.Details{Mounts: []container.DetailMount{m}}
		if err := PrepareObservationRuntime(c, foreign); err == nil {
			t.Errorf("a foreign observation bind is adopted: %+v", m)
		}
	}
	if err := RemoveObservationRuntime(c.Name); err != nil || len(*removed) != 1 || (*removed)[0] != dir {
		t.Fatalf("destroy removes the root: %v %v", err, *removed)
	}
	if err := RemoveObservationRuntime("../x"); err == nil {
		t.Fatal("an invalid name reaches a VM path")
	}
}

// The bind is controller infrastructure: inspect recognises it, and neither
// its presence nor its absence (a container created before it) is drift.
func TestObservationBindIsNeverARecreateReason(t *testing.T) {
	stubObservationRoot(t, nil)
	// The fixture container carries no agent label, so an agent config would
	// show agent drift; the bind's classification does not depend on it.
	c := validConfig()
	dir, _ := layout.AgentObserveDir(c.Name)
	command := sandbox.EntrypointArgs(c.Workdir.ContainerPath(), false, "sleep", "infinity")
	for _, withBind := range []bool{true, false} {
		mounts := buildMounts(c)
		if withBind {
			mounts = append(mounts, container.Mount{Source: dir, Destination: observe.ContainerDir})
		}
		inspector := fixtureLiveInspector(t, c, command, mounts)
		state, err := inspector.Inspect(t.Context(), c)
		if err != nil {
			t.Fatal(err)
		}
		if state.ObservationEnabled != withBind || len(state.Mounts) != 0 {
			t.Fatalf("bind=%v: enabled=%v extra mounts=%+v", withBind, state.ObservationEnabled, state.Mounts)
		}
		applier := &fakeApplier{}
		result, err := Reconcile(t.Context(), c, inspector, applier)
		if err != nil || result.ContainerRecreated || len(applier.recreateCalls) != 0 {
			t.Fatalf("bind=%v: reconcile recreated: %+v %v", withBind, result, err)
		}
	}
}
