package main

import (
	"context"
	"errors"

	"github.com/leogr/yoloctl/internal/accountstate"
	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentprofile"
	vmhost "github.com/leogr/yoloctl/internal/vm"
)

var resumeFilePresentFn = resumeFilePresent

func resumeFilePresent(ctx context.Context, cfg *agentbox.Config, spec *agentprofile.SessionSpec, id string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, vmhost.GuestProbeTimeout)
	defer cancel()
	result, err := accountStateOperation(ctx, cfg.Space, accountstate.Request{Action: "resume-file", Account: cfg.Account, Agent: cfg.Agent, Path: spec.ResumeFile, Data: []byte(id)})
	if err != nil {
		return false, err
	}
	switch result.Outcome {
	case "present":
		return true, nil
	case "absent":
		return false, nil
	default:
		return false, errors.New("saved conversation presence is unavailable")
	}
}
