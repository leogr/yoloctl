package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/spf13/cobra"
)

const (
	upgradeDriftFlag    = "upgrade-target"
	upgradeRecreateFlag = "if-upgrade-target"
)

// The snapshot ties replacement consent to the declaration, the exact
// container/run and the mode shown to the person who approved it.
type upgradeTarget struct {
	ManifestHash string
	Run          container.Run
	Running      bool
	Mode         agentbox.RuntimeMode
}

type upgradeRecreateBinding struct {
	Target upgradeTarget
	Room   string
}

type upgradeRecreateKey struct{}

func upgradeManifestHash(cfg *agentbox.Config) (string, error) {
	cfgBytes, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(cfgBytes)), nil
}

func readUpgradeTarget(ctx context.Context, b boxstate.Box) (*upgradeTarget, error) {
	if b.Config == nil || b.Container == nil {
		return nil, errors.New("box declaration or container unavailable")
	}
	details, err := container.New(b.Space).Inspect(ctx, b.Name)
	if err != nil {
		return nil, err
	}
	if container.ValidateContainerID(details.ID) != nil || b.Container.ID == "" || !strings.HasPrefix(details.ID, b.Container.ID) ||
		details.Name != b.Name || details.Labels[agentbox.LabelAgentbox] != b.Name || details.Labels[agentbox.LabelSpace] != b.Space ||
		details.Running != (b.Runtime == boxstate.RuntimeRunning) || details.Running && details.StartedAt == "" {
		return nil, errors.New("box container identity changed or is unavailable")
	}
	mode := agentbox.ClassifyRuntime(details)
	if mode.Mode == agentbox.RuntimeModeUnknown {
		return nil, fmt.Errorf("box runtime mode unavailable: %s", mode.Reason)
	}
	hash, err := upgradeManifestHash(b.Config)
	if err != nil {
		return nil, err
	}
	return &upgradeTarget{ManifestHash: hash, Run: container.RunOf(details), Running: details.Running, Mode: mode.Mode}, nil
}

func upgradeRecreateRequest(space string, row spaceUpgradeRow) (controller.Request, error) {
	if row.drift.Upgrade == nil {
		return controller.Request{}, errors.New("box replacement is missing its approved target")
	}
	binding, err := json.Marshal(upgradeRecreateBinding{Target: *row.drift.Upgrade, Room: row.room})
	if err != nil {
		return controller.Request{}, err
	}
	flags := map[string][]string{"force": {"true"}, "no-attach": {"true"}, upgradeRecreateFlag: {string(binding)}}
	if row.drift.LegacyRuntime {
		flags[legacyRemedyFlag] = []string{"true"}
	}
	if row.restore && row.kind == workloadSession {
		flags[startWorkloadFlag] = []string{"true"}
	}
	return controller.Request{ID: rand.Text(), Method: "box.recreate", Space: space, Args: []string{row.box.Name}, Dash: -1, Flags: flags}, nil
}

func readUpgradeRecreateBinding(cmd *cobra.Command, cfg *agentbox.Config, force, noAttach bool) (upgradeRecreateBinding, bool, error) {
	if !cmd.Flags().Changed(upgradeRecreateFlag) {
		return upgradeRecreateBinding{}, false, nil
	}
	raw, _ := cmd.Flags().GetString(upgradeRecreateFlag)
	var binding upgradeRecreateBinding
	if !spaceenv.Native() || !currentControllerPrincipal(cmd.Context()).Admin() || !force || !noAttach ||
		decodeBoundedJSON(strings.NewReader(raw), &binding, 4096) != nil ||
		container.ValidateContainerID(binding.Target.Run.ID) != nil || binding.Target.ManifestHash == "" {
		return upgradeRecreateBinding{}, false, asUserError(errors.New("invalid host upgrade replacement binding"))
	}
	hash, err := upgradeManifestHash(cfg)
	if err != nil {
		return upgradeRecreateBinding{}, false, err
	}
	if hash != binding.Target.ManifestHash {
		return upgradeRecreateBinding{}, false, asUserError(errors.New("box declaration changed after upgrade consent"))
	}
	if binding.Room != "" || len(cfg.SessionCommand) != 0 {
		store, err := nativeChatStoreFn()
		if err != nil {
			return upgradeRecreateBinding{}, false, err
		}
		room, err := store.Load()
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return upgradeRecreateBinding{}, false, err
		}
		isServer := room != nil && room.Server.Name == cfg.Name
		if binding.Room != "" && (!isServer || room.ID != binding.Room || room.Server.ID != cfg.ID) ||
			binding.Room == "" && isServer {
			return upgradeRecreateBinding{}, false, asUserError(errors.New("managed chat server changed after upgrade consent"))
		}
	}
	return binding, true, nil
}

func checkUpgradeRecreateTarget(ctx context.Context, details *container.Details) error {
	expected, ok := ctx.Value(upgradeRecreateKey{}).(upgradeTarget)
	if !ok {
		return nil
	}
	if details == nil || container.RunOf(details) != expected.Run ||
		details.Running != expected.Running || agentbox.ClassifyRuntime(details).Mode != expected.Mode {
		return asUserError(errors.New("box container changed after upgrade consent"))
	}
	return nil
}
