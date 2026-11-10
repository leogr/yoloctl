package main

import (
	"github.com/leogr/yoloctl/internal/config"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/spf13/cobra"
)

// resolveSpaceFlag applies the current-context resolver (explicit flag > YOLOCTL_SPACE > context pin >
// "default") to the --space flag value. Call this at the top of any
// RunE that has a --space flag; the resolver also validates the final
// name so the existing validateSpaceName call is no longer needed at
// the call site.
func resolveSpaceFlag(cmd *cobra.Command, spaceName *string) error {
	resolved, err := selectedSpace(cmd)
	if err != nil {
		// ResolveSpace errors only on invalid-name validation — always a
		// user input problem.
		return asUserError(err)
	}
	*spaceName = resolved
	return nil
}

// resolveSpacePositional returns the space name from args[0] if given,
// else from YOLOCTL_SPACE, then the global context pin, else the literal "default". Validates the
// final value. Used by `yoloctl space {create,start,stop,destroy}` —
// commands where the space name is a positional arg, not a --space
// flag. Positional name beats the pin: context only fills implicit targets
// and never overrides explicit names.
func resolveSpacePositional(args []string) (string, error) {
	if len(args) > 0 {
		if err := validateSpaceName(args[0]); err != nil {
			return "", err
		}
		return args[0], nil
	}
	if native := spaceenv.Current(); native != nil {
		return native.Space, nil
	}
	resolved, _, err := config.ResolveSpace("", false)
	if err != nil {
		return "", asUserError(err)
	}
	return resolved, nil
}

// selectedSpace is the shared flag/context selection step. Callers decide
// whether a declaration is required and how to present errors.
func selectedSpace(cmd *cobra.Command) (string, error) {
	value, changed := "", false
	if f := cmd.Flag("space"); f != nil {
		value, changed = f.Value.String(), f.Changed
	}
	if native := spaceenv.Current(); native != nil && !changed {
		return native.Space, nil
	}
	space, _, err := config.ResolveSpace(value, changed)
	return space, err
}
