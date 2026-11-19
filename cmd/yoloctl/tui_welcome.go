package main

import (
	"context"
	"errors"
	"io"
	"os"
	"slices"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/buildinfo"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/session"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/tui"
	vmhost "github.com/leogr/yoloctl/internal/vm"
)

// readWelcomeStatus runs off the UI loop. Advance only on settled observations,
// and stop as soon as a missing step is known.
func readWelcomeStatus(ctx context.Context, space string) tui.WelcomeStatus {
	ctx, cancel := context.WithTimeout(ctx, 2*vmhost.GuestProbeTimeout)
	defer cancel()
	boot, err := spaceconfig.LoadBootstrap(space)
	if errors.Is(err, os.ErrNotExist) {
		if names, err := spaceconfig.List(); err == nil && len(names) == 0 {
			return tui.WelcomeStatus{Step: tui.WelcomeNoSpace}
		}
	}
	if err != nil {
		return tui.WelcomeStatus{}
	}
	inst, found, err := findSpaceInstance(ctx, space)
	if err != nil {
		return tui.WelcomeStatus{}
	}
	if !found || inst.Status == "Stopped" {
		return tui.WelcomeStatus{Step: tui.WelcomeStopped}
	}
	if inst.Status != "Running" || boot.ImageUpdate != nil || boot.Bundle == "" {
		return tui.WelcomeStatus{}
	}
	identity, err := controller.ExecutableIdentity()
	if err != nil {
		return tui.WelcomeStatus{}
	}
	if boot.Bundle != identity {
		if olderWelcomeRelease(ctx, boot) {
			return tui.WelcomeStatus{Step: tui.WelcomeUpgrade}
		}
		return tui.WelcomeStatus{}
	}

	var boxes []controllerBoxView
	if err := controllerJSON(ctx, boot, "box.inventory", nil, nil, &boxes, io.Discard); err != nil {
		return tui.WelcomeStatus{}
	}
	return welcomeBoxStatus(boxes, func() (managedchat.Status, error) {
		var status managedchat.Status
		err := controllerJSON(ctx, boot, "chat.status", nil, nil, &status, io.Discard)
		return status, err
	})
}

// A bundle mismatch alone has no ordering (for example two dev builds or a
// host downgrade). Confirm the selected guest's release before suggesting an upgrade.
func olderWelcomeRelease(ctx context.Context, boot *spaceconfig.Bootstrap) bool {
	if !buildinfo.IsRelease(version) {
		return false
	}
	var info versionInfo
	command := vmhost.ShellCommand(ctx, boot.Name, session.VMBinaryPath, "version", "--json")
	raw := boundedBuffer{Limit: 64 << 10}
	command.Stdout = &raw
	if err := command.Run(); err != nil {
		return false
	}
	if err := decodeBoundedJSON(&raw.Buffer, &info, raw.Limit); err != nil {
		return false
	}
	return info.Bundle == boot.Bundle && buildinfo.IsRelease(info.Yoloctl) && buildinfo.CompareRelease(info.Yoloctl, version) < 0
}

func welcomeBoxStatus(boxes []controllerBoxView, readChat func() (managedchat.Status, error)) tui.WelcomeStatus {
	var operators []*agentbox.Config
	var declarations int
	for _, box := range boxes {
		if box.State.Declaration == boxstate.DeclarationMissing {
			continue
		}
		if box.State.Declaration != boxstate.DeclarationDeclared || box.Config == nil {
			return tui.WelcomeStatus{}
		}
		declarations++
		enabled, err := agentbox.OperatorEnabled(box.Config)
		if err != nil {
			return tui.WelcomeStatus{}
		}
		if enabled {
			operators = append(operators, box.Config)
		}
	}
	if len(operators) == 0 {
		return tui.WelcomeStatus{Step: tui.WelcomeNoOperator}
	}

	chat, err := readChat()
	if err != nil {
		return tui.WelcomeStatus{}
	}
	if chat.Room == nil || chat.SetupRequired {
		return tui.WelcomeStatus{Step: tui.WelcomeNoChat}
	}
	if !chat.ServerReady {
		return tui.WelcomeStatus{}
	}
	if !slices.ContainsFunc(boxes, func(box controllerBoxView) bool {
		return box.State.Declaration == boxstate.DeclarationDeclared && box.Config != nil &&
			box.Config.Name == chat.Room.Server.Name && box.Config.ID == chat.Room.Server.ID
	}) {
		return tui.WelcomeStatus{} // The two reads disagree about the server.
	}
	declarations--
	for _, op := range operators {
		for _, member := range chat.Room.Members {
			if op.ID != "" && !member.Revoked && member.Name == op.Name && member.BoxID == op.ID {
				if declarations == 1 {
					return tui.WelcomeStatus{Step: tui.WelcomeOnlyOperator, Operator: op.Name}
				}
				return tui.WelcomeStatus{Step: tui.WelcomeReady}
			}
		}
	}
	return tui.WelcomeStatus{Step: tui.WelcomeEnroll, Operator: operators[0].Name}
}
