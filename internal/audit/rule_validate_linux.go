package audit

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"strconv"
)

var falcoWarnings = regexp.MustCompile(`(?m)^([0-9]+) Warnings?:\s*$`)

// ValidateRuleSet invokes the same pinned installed Falco once, with its fixed
// configuration/plugins and no capture or validation-phase timeout. Caller
// cancellation uses the collector's existing process ownership machinery.
func ValidateRuleSet(ctx context.Context, binary, config string) (Validation, error) {
	cmd := falcoCommand(ctx, binary, "-c", config, "--dry-run")
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	err := cmd.Run()
	result := Validation{Diagnostics: output.String()}
	// These are Falco's own per-load header counts, not yoloctl rule parsing or
	// an assertion that all rules remain active after subsequent loads/selections.
	for _, match := range falcoWarnings.FindAllStringSubmatch(result.Diagnostics, -1) {
		count, parseErr := strconv.ParseUint(match[1], 10, 64)
		if parseErr != nil {
			return result, parseErr
		}
		result.Warnings += count
	}
	if err != nil {
		return result, errors.Join(errors.New("falco rejected the audit rules"), err, ctx.Err())
	}
	return result, ctx.Err()
}
