package main

import (
	"context"
	"errors"
	"os"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/port"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

// Private VM infrastructure only. The fixed root context and current disk are
// verified independently of argv, and no Cobra/operator/host setup is reached.
func runOpenShellForwardInternal(ctx context.Context, args []string) error {
	if os.Geteuid() != 0 || os.Getegid() != 0 {
		return errors.New("OpenShell forward worker requires native VM root")
	}
	spec, binding, err := port.ParseForwardWorker(args)
	if err != nil {
		return err
	}
	if err := controller.LoadRuntime(ctx); err != nil {
		return err
	}
	client := container.New(spaceenv.Current().Space)
	return agentbox.ServeNativeOpenShellForward(ctx, client, spec, binding, controller.VerifyRuntimeStorage)
}
