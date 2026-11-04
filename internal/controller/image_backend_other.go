//go:build !linux

package controller

import (
	"context"
	"errors"
)

func NewNativeImageBackend(context.Context) (*NativeImageBackend, error) {
	return nil, errors.New("native image jobs require the Linux VM controller")
}
