package main

import (
	"context"
	"errors"

	"github.com/leogr/yoloctl/internal/firecracker"
	"github.com/leogr/yoloctl/internal/sshtransport"
)

func runVMInternal(ctx context.Context, args []string) error {
	if len(args) >= 4 && args[0] == "ssh" {
		if args[3] != "pipe" && args[3] != "tty" {
			return errors.New("invalid SSH transport mode")
		}
		err := firecracker.SSH(ctx, args[1], args[2], args[4:], args[3] == "tty")
		if code, ok := sshtransport.ExitStatus(err); ok {
			return passthroughExitError{err: err, code: code}
		}
		return err
	}
	if len(args) != 2 {
		return errors.New("invalid private VM command")
	}
	switch args[0] {
	case "helper":
		return firecracker.Helper(ctx, args[1])
	case "proxy":
		return firecracker.Proxy(ctx, args[1])
	default:
		return errors.New("unknown private VM command")
	}
}
