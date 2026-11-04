package boxstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentsession"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/session"
	vmhost "github.com/leogr/yoloctl/internal/vm"
)

func saveBox(t *testing.T, space, name string) *agentbox.Config {
	t.Helper()
	cfg := &agentbox.Config{
		SchemaVersion: agentbox.SchemaVersion,
		Name:          name,
		Space:         space,
		Agent:         "codex",
		Account:       "default",
		Image:         "img",
		Features:      []string{"codex"},
		Workdir: agentbox.Workdir{
			Mode:      agentbox.WorkdirModeHost,
			Source:    "/tmp/repo",
			Canonical: "/tmp/repo",
		},
	}
	if err := cfg.Save(); err != nil {
		t.Fatalf("save box: %v", err)
	}
	return cfg
}

func testResolver(containers []container.Container) Resolver {
	return Resolver{
		ListSpaces: func(context.Context) ([]vmhost.Instance, error) {
			return []vmhost.Instance{{Name: "default", Status: "Running"}}, nil
		},
		ListContainers: func(context.Context, string) ([]container.Container, error) {
			return containers, nil
		},
		ProbeSession: func(_ context.Context, _ string, name string, running bool) SessionInfo {
			if name == "lost" {
				return SessionInfo{State: SessionLost}
			}
			if name == "exited" {
				return SessionInfo{State: SessionExited, ExitCode: 7, ExitedAt: time.Unix(10, 0)}
			}
			if running {
				return SessionInfo{State: SessionRunning}
			}
			return SessionInfo{State: SessionNone}
		},
		InspectActual: func(_ context.Context, cfg *agentbox.Config) (agentbox.ActualState, error) {
			return agentbox.ActualState{
				Exists:        true,
				Image:         cfg.Image,
				ReadOnlyWD:    !cfg.WorkdirWritable(),
				WorkdirSource: cfg.Workdir.MountSource(),
				WorkdirDest:   cfg.Workdir.ContainerPath(),
				Labels:        map[string]string{agentbox.LabelAgent: cfg.Agent},
				PolicyContent: []byte("policy"),
			}, nil
		},
		ComposePolicy: func(*agentbox.Config) ([]byte, error) {
			return []byte("policy"), nil
		},
		// Mirror the fake InspectActual, which reports actual.Image == cfg.Image:
		// desired == actual on the image axis, so these tests exercise the other
		// drift dimensions without the composed-image resolver in the way.
		ResolvedImage: func(c *agentbox.Config) (string, bool, error) {
			return c.Image, false, nil
		},
	}
}

func TestResolveSpaceUnavailableRuntime(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saveBox(t, "default", "coder")
	r := testResolver(nil)
	r.ListSpaces = func(context.Context) ([]vmhost.Instance, error) {
		return []vmhost.Instance{{Name: "default", Status: "Stopped"}}, nil
	}

	boxes, err := r.ResolveSpace(context.Background(), "default", RuntimeSummary)
	if err != nil {
		t.Fatal(err)
	}
	b := boxes[0]
	if b.Runtime != RuntimeUnavailable || b.State != DisplayUnavailable {
		t.Fatalf("unexpected state: %+v", b)
	}
}

func TestResolveSpaceRuntimeStatesAndOrphan(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saveBox(t, "default", "coder")
	saveBox(t, "default", "stopped")
	saveBox(t, "default", "lost")
	saveBox(t, "default", "exited")
	containers := []container.Container{
		{Name: "coder", Status: "Up 10 seconds", Image: "img"},
		{Name: "stopped", Status: "Exited (0)", Image: "img"},
		{Name: "lost", Status: "Up 1 minute", Image: "img"},
		{Name: "exited", Status: "Up 1 minute", Image: "img"},
		{Name: "orphan", Status: "Up 1 minute", Image: "img"},
	}

	boxes, err := testResolver(containers).ResolveSpace(context.Background(), "default", RuntimeSummary)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Box{}
	for _, b := range boxes {
		byName[b.Name] = b
	}
	if got := byName["coder"]; got.Runtime != RuntimeRunning || got.Session.State != SessionRunning || got.State != DisplayRunning {
		t.Fatalf("coder state = %+v", got)
	}
	if got := byName["stopped"]; got.Runtime != RuntimeStopped || got.State != DisplayStopped {
		t.Fatalf("stopped state = %+v", got)
	}
	if got := byName["lost"]; got.Session.State != SessionLost || got.State != DisplayLost {
		t.Fatalf("lost state = %+v", got)
	}
	if got := byName["exited"]; got.Session.State != SessionExited || got.State != DisplayExited {
		t.Fatalf("exited state = %+v", got)
	}
	if got := byName["orphan"]; got.Declaration != DeclarationMissing || got.Runtime != RuntimeOrphaned || got.State != DisplayOrphaned {
		t.Fatalf("orphan state = %+v", got)
	}
}

func TestResolveSpaceFullDiffDrift(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saveBox(t, "default", "coder")
	r := testResolver([]container.Container{{Name: "coder", Status: "Up", Image: "old"}})
	r.InspectActual = func(_ context.Context, cfg *agentbox.Config) (agentbox.ActualState, error) {
		return agentbox.ActualState{
			Exists:        true,
			Image:         "old",
			ReadOnlyWD:    !cfg.WorkdirWritable(),
			WorkdirSource: cfg.Workdir.MountSource(),
			WorkdirDest:   cfg.Workdir.ContainerPath(),
			Labels:        map[string]string{agentbox.LabelAgent: cfg.Agent},
			PolicyContent: []byte("policy"),
		}, nil
	}

	boxes, err := r.ResolveSpace(context.Background(), "default", FullDiff)
	if err != nil {
		t.Fatal(err)
	}
	b := boxes[0]
	if b.Drift != DriftNeedsRecreate || b.State != DisplayDrifted {
		t.Fatalf("unexpected drift state: %+v", b)
	}
}

func TestResolveSpaceInvalidDeclaration(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := saveBox(t, "default", "coder")
	path := cfg.Path()
	// Save rejects invalid schema versions, so write the invalid file directly.
	if err := os.WriteFile(path, []byte("schema_version: 999\nname: coder\nspace: default\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	boxes, err := testResolver(nil).ResolveSpace(context.Background(), "default", RuntimeSummary)
	if err != nil {
		t.Fatal(err)
	}
	if got := boxes[0]; got.Declaration != DeclarationInvalid || got.State != DisplayInvalid {
		t.Fatalf("invalid declaration state = %+v", got)
	}
}

func TestRuntimeInventoryIsAuthoritativeWithoutSessionProbes(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saveBox(t, "default", "coder")
	r := testResolver([]container.Container{{Name: "coder", Status: "Up 1 minute"}, {Name: "orphan", Status: "Exited (0)"}})
	r.ProbeSession = func(context.Context, string, string, bool) SessionInfo {
		t.Fatal("inventory probed a session")
		return SessionInfo{}
	}
	boxes, err := r.ResolveSpace(t.Context(), "default", RuntimeInventory)
	if err != nil || len(boxes) != 2 {
		t.Fatalf("inventory: %+v %v", boxes, err)
	}
	r.ListContainers = func(context.Context, string) ([]container.Container, error) {
		return nil, fmt.Errorf("container probe failed")
	}
	if _, err := r.ResolveSpace(t.Context(), "default", RuntimeInventory); err == nil {
		t.Fatal("inventory degraded a failed runtime probe")
	}
	r.ListSpaces = func(context.Context) ([]vmhost.Instance, error) { return nil, nil }
	if _, err := r.ResolveSpace(t.Context(), "default", RuntimeInventory); err == nil {
		t.Fatal("inventory treated missing VM as empty runtime")
	}
}

// A running session's build comes only from its holder's explicit answer to
// the stats handshake; a failure to ask is unknown, never current or other.
// The field is omitted for sessions that were not probed.
func TestSessionBuildFollowsTheHolderAnswer(t *testing.T) {
	for _, c := range []struct {
		err  error
		want SessionBuild
	}{
		{nil, SessionBuildCurrent},
		{fmt.Errorf("probe: %w", session.ErrBuildMismatch), SessionBuildOther},
		{errors.New("session stats unavailable: EOF"), SessionBuildUnknown},
		{context.DeadlineExceeded, SessionBuildUnknown},
	} {
		if got := sessionBuild(c.err); got != c.want {
			t.Errorf("%v: %s, want %s", c.err, got, c.want)
		}
	}
	for _, c := range []struct {
		info SessionInfo
		want string
	}{
		{SessionInfo{State: SessionRunning, Build: SessionBuildOther, Holder: "7@t"}, `{"state":"running","build":"other","holder":"7@t","exited_at":"0001-01-01T00:00:00Z"}`},
		{SessionInfo{State: SessionNone}, `{"state":"none","exited_at":"0001-01-01T00:00:00Z"}`},
	} {
		if got, err := json.Marshal(c.info); err != nil || string(got) != c.want {
			t.Errorf("%+v encoded %s %v", c.info, got, err)
		}
	}
}

// ResolveNamed and ResolveOne list the space once and run the per-box
// session probe, drift (inspection, policy, image) and conversation only for
// the requested identities; each result equals that box's ResolveSpace
// entry, in request order, with a missing name reported as missing.
func TestResolveNamedLimitsPerBoxWorkToTheNamedBoxes(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, name := range []string{"alpha", "beta", "gamma"} {
		saveBox(t, "default", name)
	}
	containers := []container.Container{{Name: "alpha", Status: "Up"}, {Name: "beta", Status: "Up"}, {Name: "gamma", Status: "Exited (0)"}, {Name: "orphan", Status: "Up"}}
	spy := func(work map[string][]string) Resolver {
		r := testResolver(containers)
		list, probe, inspect := r.ListContainers, r.ProbeSession, r.InspectActual
		r.ListContainers = func(ctx context.Context, space string) ([]container.Container, error) {
			work["list"] = append(work["list"], space)
			return list(ctx, space)
		}
		r.ProbeSession = func(ctx context.Context, space, name string, running bool) SessionInfo {
			work["probe"] = append(work["probe"], name)
			return probe(ctx, space, name, running)
		}
		r.InspectActual = func(ctx context.Context, cfg *agentbox.Config) (agentbox.ActualState, error) {
			work["inspect"] = append(work["inspect"], cfg.Name)
			return inspect(ctx, cfg)
		}
		r.ComposePolicy = func(cfg *agentbox.Config) ([]byte, error) {
			work["policy"] = append(work["policy"], cfg.Name)
			return []byte("policy"), nil
		}
		r.ResolvedImage = func(cfg *agentbox.Config) (string, bool, error) {
			work["image"] = append(work["image"], cfg.Name)
			return cfg.Image, false, nil
		}
		r.Conversation = func(b Box) *agentsession.Status {
			work["conversation"] = append(work["conversation"], b.Name)
			return nil
		}
		return r
	}
	whole := map[string][]string{}
	all, err := spy(whole).ResolveSpace(context.Background(), "default", FullDiff)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Box{}
	for _, b := range all {
		byName[b.Name] = b
	}
	work := map[string][]string{}
	got, err := spy(work).ResolveNamed(context.Background(), "default", []string{"gamma", "orphan", "alpha", "gamma", "nobody"}, FullDiff)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || got[0].Name != "gamma" || got[1].Name != "orphan" || got[2].Name != "alpha" || got[3].Name != "nobody" {
		t.Fatalf("results: %+v", got)
	}
	for _, b := range got[:3] {
		if !reflect.DeepEqual(b, byName[b.Name]) {
			t.Fatalf("%s differs from its ResolveSpace entry:\n%+v\n%+v", b.Name, b, byName[b.Name])
		}
	}
	if b := got[3]; b.Declaration != DeclarationMissing || b.Runtime != RuntimeMissing || b.Drift != DriftUnknown || b.Session.State != SessionUnknown {
		t.Fatalf("missing name: %+v", b)
	}
	want := map[string][]string{
		"list":         {"default"},
		"probe":        {"alpha", "gamma", "orphan"},
		"inspect":      {"alpha", "gamma"},
		"policy":       {"alpha", "gamma"},
		"image":        {"alpha", "gamma"},
		"conversation": {"alpha", "gamma"},
	}
	for kind, names := range want {
		if got := slices.Sorted(slices.Values(work[kind])); !slices.Equal(got, names) {
			t.Errorf("%s ran for %v, want %v", kind, got, names)
		}
	}
	if len(work) != len(want) {
		t.Errorf("unexpected work: %v", work)
	}
	one := map[string][]string{}
	b, err := spy(one).ResolveOne(context.Background(), "default", "beta", FullDiff)
	if err != nil || !reflect.DeepEqual(b, byName["beta"]) {
		t.Fatalf("ResolveOne: %v %+v", err, b)
	}
	for _, kind := range []string{"probe", "inspect", "policy", "image", "conversation"} {
		if !slices.Equal(one[kind], []string{"beta"}) {
			t.Errorf("ResolveOne %s ran for %v", kind, one[kind])
		}
	}
}
