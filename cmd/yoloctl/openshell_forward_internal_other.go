//go:build !linux

package main

import (
	"context"
	"errors"
)

func runOpenShellForwardInternal(context.Context, []string) error {
	return errors.New("OpenShell forward worker requires Linux VM infrastructure")
}
