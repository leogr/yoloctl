//go:build !linux

package audit

import (
	"context"
	"errors"
)

func ProbeSupport() error                                                  { return errors.New("falco runs only inside a Linux space VM") }
func Collect(context.Context, string, string, string, ServiceConfig) error { return ProbeSupport() }
