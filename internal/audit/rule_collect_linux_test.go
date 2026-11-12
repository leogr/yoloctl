package audit

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/safepath"
)

func TestCollectorUsesSelectedValidatedGeneration(t *testing.T) {
	root, run, tools := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(tools, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(tools, "bin", "falco")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$0.args\"\ncase \" $* \" in *' -M 2 '*) exit 0;; esac\nexec sleep 60\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	store := RuleStore{Root: root}
	validations := 0
	set, _, _, err := store.Prepare(t.Context(), "", RuleChange{Kind: "edit", Body: strings.NewReader("rules:\n- disable: {rule: '*'}\n")}, tools, filepath.Join(run, "metadata.tsv"), func(context.Context, string, string) (Validation, error) { validations++; return Validation{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	oldProbe := collectProbeSupport
	collectProbeSupport = func() error { return nil }
	t.Cleanup(func() { collectProbeSupport = oldProbe })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	service := ServiceConfig{Epoch: "selected", Version: Version, RulesSHA256: RulesDigest(), RulesGeneration: set.Generation, EffectiveSHA256: set.Fingerprint}
	go func() { done <- Collect(ctx, root, run, tools, service) }()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	loaded := false
	for !loaded {
		select {
		case <-deadline.C:
			t.Fatal("fixture did not reach selected loaded health")
		case err := <-done:
			t.Fatal("collector stopped before loaded health", err)
		case <-ticker.C:
			raw, _ := safepath.ReadFile(run, "health.json", 4096)
			var health Health
			if json.Unmarshal(raw, &health) == nil && health.Loaded {
				if health.RulesGeneration != set.Generation || health.EffectiveSHA256 != set.Fingerprint {
					t.Fatal("unbound health", health)
				}
				args, _ := os.ReadFile(binary + ".args")
				loaded = len(strings.Split(strings.TrimSpace(string(args)), "\n")) == 2
			}
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-deadline.C:
		t.Fatal("collector did not stop")
	}
	raw, err := os.ReadFile(binary + ".args")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 || strings.Contains(string(raw), "-V") || strings.Contains(string(raw), "--dry-run") || validations != 1 {
		t.Fatalf("revalidated instead of runtime readiness/capture: %q", raw)
	}
	// A mismatched selected fingerprint refuses before starting any process.
	if err := os.Remove(binary + ".args"); err != nil {
		t.Fatal(err)
	}
	service.EffectiveSHA256 = strings.Repeat("a", 64)
	if err := Collect(t.Context(), root, run, tools, service); err == nil {
		t.Fatal("mismatched fingerprint accepted")
	}
	if _, err := os.Stat(binary + ".args"); !os.IsNotExist(err) {
		t.Fatal("mismatched service launched Falco")
	}
}
