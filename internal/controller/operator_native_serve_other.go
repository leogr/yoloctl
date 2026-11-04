//go:build !linux

package controller

import (
	"errors"

	"github.com/leogr/yoloctl/internal/operator"
)

func defaultNativeOperatorListener(operator.RegistrationIdentity) (nativeServedListener, error) {
	return nil, errors.New("native operator listeners require Linux")
}
