package main

import (
	"context"
	"fmt"
	"io"
	"strconv"

	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/resources"
	"github.com/leogr/yoloctl/internal/vmapi"
	"github.com/spf13/cobra"
)

var readHostCPU = resources.ReadHostCPU

func selectSpaceCPUs(cmd *cobra.Command, backend string) (int, error) {
	value, _ := cmd.Flags().GetString("cpus")
	return resolveSpaceCPUs(cmd.Context(), backend, value, cmd.ErrOrStderr())
}

// resolveSpaceCPUs chooses a new space's boot vCPU count once. Only creation
// measures the host: start, recreate and import keep the saved count, and
// space set accepts only explicit counts.
func resolveSpaceCPUs(ctx context.Context, backend, value string, errw io.Writer) (int, error) {
	if value != "auto" {
		n, err := strconv.ParseUint(value, 10, 31)
		if err != nil || n == 0 {
			return 0, asUserError(fmt.Errorf("invalid --cpus %q; use auto or a positive integer", value))
		}
		cliInfo(logging.Host, "VM CPUs: %d (explicit)", n)
		return int(n), nil
	}
	host, err := readHostCPU(ctx)
	if err != nil {
		return 0, fmt.Errorf("measure host CPUs (or pass --cpus N explicitly): %w", err)
	}
	maximum := vmapi.MaxCPUs(backend)
	n, clipped, err := resources.VMCPUs(host, maximum)
	if err != nil {
		return 0, fmt.Errorf("%w; pass --cpus N explicitly", err)
	}
	basis := host.Describe()
	if clipped {
		basis += fmt.Sprintf(", %s maximum %d", backend, maximum)
	}
	cliInfo(logging.Host, "VM CPUs: %d (automatic: %s; override with --cpus)", n, basis)
	return n, nil
}
