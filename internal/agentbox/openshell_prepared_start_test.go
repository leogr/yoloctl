package agentbox

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestNativeStartCheckpointIsOneUseAndScopeBound(t *testing.T) {
	cfg := validConfig()
	calls, closes := 0, 0
	s := &NativePreparedStart{space: cfg.Space, name: cfg.Name, boxID: cfg.ID,
		check: func(context.Context) error { return nil }, start: func(context.Context) error { calls++; return &NativeEffectsUncertain{Cause: errors.New("lost Start")} }, close: func() error { closes++; return nil }}
	wrong := *cfg
	wrong.Name = "other"
	if err := s.Start(t.Context(), &wrong); err == nil || calls != 0 {
		t.Fatal("foreign box consumed checkpoint", err, calls)
	}
	if err := s.Start(t.Context(), cfg); err == nil {
		t.Fatal("lost Start became success")
	}
	if err := s.Start(t.Context(), cfg); err == nil || calls != 1 {
		t.Fatal("checkpoint retried lost launch", err, calls)
	}
	_ = s.Close()
	_ = s.Close()
	if closes != 1 || s.Check(t.Context(), cfg) == nil {
		t.Fatal("closed checkpoint remained usable", closes)
	}
	if (&NativePreparedStart{}).Start(t.Context(), cfg) == nil {
		t.Fatal("zero checkpoint launched")
	}
}

func TestNativeStartCheckpointFormattingDoesNotRevealClosures(t *testing.T) {
	if strings.Contains((&NativePreparedStart{}).GoString(), "check:") {
		t.Fatal("checkpoint exposed private contents")
	}
}

// The checkpoint models a box whose recorded policy (A) differs from its saved
// declaration (B); the prepared start never reads A.
type startCheckpointFixture struct {
	recorded []byte
	closed   int
}

func (c *startCheckpointFixture) OriginalPolicy() []byte { return bytes.Clone(c.recorded) }
func (c *startCheckpointFixture) Close() error           { c.closed++; return nil }

// A new run of an existing native box qualifies and launches the saved policy
// B first, and only B, for explicit start and revive alike. Revive relaunches
// the observed forwards; explicit start launches without forwards (its
// desired reconcile follows).
func TestNativeStartLaunchesTheSavedPolicyNotTheRecordedOne(t *testing.T) {
	for _, revive := range []bool{false, true} {
		for _, observedPorts := range [][]Port{nil, {{Published: 9000, Target: 7000}}} {
			t.Run(map[bool]string{false: "explicit", true: "revive"}[revive]+map[bool]string{true: "/empty", false: "/observed"}[len(observedPorts) == 0], func(t *testing.T) {
				cfg := validConfig()
				cfg.Ports = []Port{{Published: 9300, Target: 8300}}
				saved := mustCompose(t, cfg)
				checkpoint := &startCheckpointFixture{recorded: append([]byte("# recorded A\n"), saved...)}
				inventory := append([]Port(nil), observedPorts...)
				calls, launches := 0, 0
				prepared, err := retainNativePreparedStart(t.Context(), cfg, revive, checkpoint, nativePreparedStartActions{
					qualify: func(_ context.Context, _ *Config, p []byte) (*Config, error) {
						calls++
						if !bytes.Equal(p, saved) {
							t.Fatal("qualified a policy other than the saved one", string(p))
						}
						p[0] = 'X' // the callback cannot mutate the retained policy
						result := *cfg
						result.Ports = append([]Port(nil), inventory...)
						return &result, nil
					}, preserve: func(context.Context) error { return nil },
					launch: func(_ context.Context, c *Config, p []byte) error {
						launches++
						want := []Port(nil)
						if revive {
							want = observedPorts
						}
						if !bytes.Equal(p, saved) || !reflect.DeepEqual(c.Ports, want) {
							t.Fatal("launched another policy or inferred forwards from the declaration", string(p), c.Ports, want)
						}
						return nil
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				inventory = nil // actual withdrawal happens after retention
				if err := prepared.Start(t.Context(), cfg); err != nil {
					t.Fatal(err)
				}
				_ = prepared.Close()
				if calls != 2 || launches != 1 || checkpoint.closed != 1 {
					t.Fatal("prepared start lifecycle changed", calls, launches, checkpoint.closed)
				}
			})
		}
	}
}

// A saved policy the native qualification rejects launches nothing: the
// recorded policy is no fallback.
func TestNativeStartRejectedSavedPolicyLaunchesNothing(t *testing.T) {
	cfg := validConfig()
	checkpoint := &startCheckpointFixture{recorded: []byte("# recorded A\n")}
	refused := errors.New("saved policy refused by the native qualification")
	launches := 0
	prepared, err := retainNativePreparedStart(t.Context(), cfg, true, checkpoint, nativePreparedStartActions{
		qualify:  func(context.Context, *Config, []byte) (*Config, error) { return nil, refused },
		preserve: func(context.Context) error { return nil },
		launch:   func(context.Context, *Config, []byte) error { launches++; return nil },
	})
	if !errors.Is(err, refused) || prepared != nil || launches != 0 || checkpoint.closed != 1 {
		t.Fatal("rejected saved policy launched or leaked its checkpoint", err, launches, checkpoint.closed)
	}
}

// Explicit start then its desired reconcile: the prepared start already ran
// the saved policy, so the reconcile restarts nothing. There is no second
// launch from A to B (the parent ran the recorded A, then a policy restart).
func TestNativeStartThenReconcileHasNoPolicyRestartLeg(t *testing.T) {
	cfg := validConfig()
	saved := mustCompose(t, cfg)
	checkpoint := &startCheckpointFixture{recorded: append([]byte("# recorded A\n"), saved...)}
	inspector := &fakeInspector{state: ActualState{Exists: true, Image: mustResolveImage(t, cfg), PolicyContent: checkpoint.OriginalPolicy(), WorkloadPoolKnown: true, WorkloadPool: true}}
	prepared, err := retainNativePreparedStart(t.Context(), cfg, false, checkpoint, nativePreparedStartActions{
		qualify:  func(context.Context, *Config, []byte) (*Config, error) { result := *cfg; return &result, nil },
		preserve: func(context.Context) error { return nil },
		launch: func(_ context.Context, _ *Config, p []byte) error {
			inspector.state.PolicyContent = bytes.Clone(p) // the runtime now runs what was launched
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = prepared.Close() }()
	if err := prepared.Start(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	applier := &fakeApplier{}
	if _, err := ReconcileForStart(t.Context(), cfg, inspector, applier); err != nil {
		t.Fatal(err)
	}
	if len(applier.restartCalls) != 0 || len(applier.recreateCalls) != 0 || !bytes.Equal(inspector.state.PolicyContent, saved) {
		t.Fatal("start ran another policy and reconciled it afterwards", len(applier.restartCalls), len(applier.recreateCalls))
	}
}
