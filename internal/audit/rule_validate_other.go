//go:build !linux

package audit

import (
	"context"
	"errors"
)

func ValidateRuleSet(context.Context, string, string) (Validation, error) {
	return Validation{}, errors.New("Falco validation requires the native Linux controller")
}
