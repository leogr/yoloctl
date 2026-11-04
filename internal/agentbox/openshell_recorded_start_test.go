package agentbox

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestNativeStartCheckpointIsOneUseAndScopeBound(t *testing.T) {
	cfg := validConfig()
	calls, closes := 0, 0
	s := &NativeRecordedStart{space: cfg.Space, name: cfg.Name, boxID: cfg.ID,
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
	if (&NativeRecordedStart{}).Start(t.Context(), cfg) == nil {
		t.Fatal("zero checkpoint launched")
	}
}

func TestNativeStartForwardRestorationRetainsCallerIntentIncludingEmpty(t *testing.T) {
	for _, declared := range [][]Port{nil, {{Published: 9000, Target: 8000}}} {
		cfg := validConfig()
		cfg.Ports = append([]Port(nil), declared...)
		observed := *cfg
		observed.Ports = []Port{{Published: 9100, Target: 7100}}
		explicit := (LiveApplier{}).WithDeclaredForwardRestoration(cfg)
		cfg.Ports = []Port{{Published: 9300, Target: 8300}} // caller cannot rewrite captured intent
		for _, test := range []struct {
			name string
			a    LiveApplier
			want []Port
		}{{"explicit", explicit, declared}, {"running-reconcile", LiveApplier{}, observed.Ports}} {
			t.Run(test.name, func(t *testing.T) {
				got, err := test.a.nativeRollbackConfig(cfg, &observed)
				if err != nil || !reflect.DeepEqual(got.Ports, test.want) {
					t.Fatal("rollback selected wrong caller forwarding intent", got, err, test.want)
				}
				if len(got.Ports) > 0 && test.name == "explicit" {
					got.Ports[0].Target = 1
					again, _ := test.a.nativeRollbackConfig(cfg, &observed)
					if again.Ports[0].Target != 8000 {
						t.Fatal("mutable restoration intent")
					}
				}
			})
		}
		wrong := *cfg
		wrong.ID = "another"
		if _, err := explicit.nativeRollbackConfig(&wrong, &observed); err == nil {
			t.Fatal("foreign scope accepted forwarding intent")
		}
	}
}

// Actual ReconcileForStart + native policy restart protocol composition lives
// in the Linux test below; these descriptor-free checkpoint tests are portable.
func TestNativeStartCheckpointFormattingDoesNotRevealClosures(t *testing.T) {
	if strings.Contains((&NativeRecordedStart{}).GoString(), "check:") {
		t.Fatal("checkpoint exposed private contents")
	}
}

type recordedCheckpointFixture struct {
	policy []byte
	closed int
}

func (c *recordedCheckpointFixture) OriginalPolicy() []byte { return append([]byte(nil), c.policy...) }
func (c *recordedCheckpointFixture) Close() error           { c.closed++; return nil }
func TestNativeStartRetainsRecordedPolicyAndObservedIntentBeforeWithdrawal(t *testing.T) {
	for _, revive := range []bool{false, true} {
		for _, originalPorts := range [][]Port{nil, {{Published: 9000, Target: 7000}}} {
			t.Run(map[bool]string{false: "explicit", true: "revive"}[revive]+map[bool]string{true: "/empty", false: "/changed"}[len(originalPorts) == 0], func(t *testing.T) {
				cfg := validConfig()
				cfg.Ports = []Port{{Published: 9300, Target: 8300}}
				checkpoint := &recordedCheckpointFixture{policy: []byte("recorded A")}
				inventory := append([]Port(nil), originalPorts...)
				calls, launches := 0, 0
				prepared, err := retainNativeRecordedStart(t.Context(), cfg, revive, checkpoint, nativeRecordedStartActions{
					qualify: func(_ context.Context, _ *Config, p []byte) (*Config, error) {
						calls++
						if string(p) != "recorded A" {
							t.Fatal("revive replaced recorded A with desired policy", string(p))
						}
						p[0] = 'X'
						result := *cfg
						result.Ports = append([]Port(nil), inventory...)
						return &result, nil
					}, preserve: func(context.Context) error { return nil },
					launch: func(_ context.Context, c *Config, p []byte) error {
						launches++
						want := []Port(nil)
						if revive {
							want = originalPorts
						}
						if string(p) != "recorded A" || !reflect.DeepEqual(c.Ports, want) {
							t.Fatal("recorded start inferred intent from emptied inventory/desired policy", string(p), c.Ports, want)
						}
						return nil
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				inventory = nil // actual ACK withdrawal happens after retention
				if err := prepared.Start(t.Context(), cfg); err != nil {
					t.Fatal(err)
				}
				_ = prepared.Close()
				if calls != 2 || launches != 1 || checkpoint.closed != 1 {
					t.Fatal("recorded checkpoint lifecycle changed", calls, launches, checkpoint.closed)
				}
			})
		}
	}
}
