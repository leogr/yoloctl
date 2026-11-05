//go:build !linux

package main

import (
	"context"
	"errors"
	"io"

	"github.com/leogr/yoloctl/internal/controller"
)

func runControllerAudit(context.Context, controller.Request, io.Reader, io.Writer) (bool, error) {
	return false, nil
}
func collectNativeAudit(context.Context) error   { return errors.New("audit infrastructure needs Linux") }
func reconcileNativeAudit(context.Context) error { return nil }

func startAuditRetention(context.Context, *controller.Server) func() { return func() {} }
