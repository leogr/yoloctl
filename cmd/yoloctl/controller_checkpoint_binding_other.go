//go:build !linux

package main

import (
	"context"
	"github.com/leogr/yoloctl/internal/workercheckpoint"
)

func bindControllerCheckpoint(context.Context, *workerLaunchCandidate, *workercheckpoint.Channel) {}
