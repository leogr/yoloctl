package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/leogr/yoloctl/assets"
	"github.com/leogr/yoloctl/internal/buildinfo"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/spf13/cobra"
)

// versionInfo is the aggregated result of all the version probes.
// Pinned versions (yoloctl, openshell-sandbox) are always present;
// `limactl` is best-effort (the user might not have it on PATH yet).
type versionInfo struct {
	Channel            string `json:"channel"`
	ConfigRoot         string `json:"config_root"`
	CacheRoot          string `json:"cache_root"`
	Yoloctl            string `json:"yoloctl"`
	OpenshellSandbox   string `json:"openshell_sandbox"`
	Limactl            string `json:"limactl,omitempty"`
	Bundle             string `json:"bundle,omitempty"`
	ControllerProtocol int    `json:"controller_protocol"`
}

func newVersionCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print yoloctl, openshell-sandbox, and Lima versions",
		Long: `Reports the three versions that pin yoloctl's behavior:

  yoloctl            this binary
  openshell-sandbox  native runtime target; executable hashes come from
                     the bundled OpenShell qualification
  limactl            host-side binary version

  yoloctl version
  yoloctl version --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			info := collectVersions(cmd.Context())
			var err error
			info.ConfigRoot, err = layout.ConfigRoot()
			if err != nil {
				return err
			}
			info.CacheRoot, err = layout.CacheRoot()
			if err != nil {
				return err
			}
			if asJSON {
				return writeVersionJSON(cmd.OutOrStdout(), info)
			}
			return writeVersionText(cmd.OutOrStdout(), info)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit JSON instead of the table view")
	return cmd
}

// collectVersions runs the per-source probes sequentially.
func collectVersions(ctx context.Context) versionInfo {
	info := versionInfo{
		Yoloctl:            version,
		Channel:            buildinfo.Channel(),
		OpenshellSandbox:   assets.SandboxVersion,
		ControllerProtocol: controller.ProtocolVersion,
	}
	info.Bundle, _ = controller.ExecutableIdentity()
	if v, ok := limactlVersion(ctx); ok {
		info.Limactl = v
	}
	return info
}

// limactlVersion runs `limactl --version` and parses the third token.
// Lima emits "limactl version <semver-or-sha>" on a single stdout line.
// Falls back to ("", false) if limactl isn't on PATH or output is
// unexpected — both surface as "Limactl: (unavailable)" in the table
// and an omitted JSON key.
func limactlVersion(ctx context.Context) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	command := vmhost.Command(ctx, "--version")
	if home, err := layout.LimaHome(); err == nil {
		command.Env = append(os.Environ(), "LIMA_HOME="+home)
	}
	command.WaitDelay = 100 * time.Millisecond
	out, err := command.Output()
	if err != nil {
		return "", false
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) < 3 {
		return "", false
	}
	return fields[2], true
}

func writeVersionText(w io.Writer, info versionInfo) error {
	_, _ = fmt.Fprintf(w, "channel:            %s\nconfig root:        %s\ncache root:         %s\n", info.Channel, info.ConfigRoot, info.CacheRoot)
	_, _ = fmt.Fprintf(w, "yoloctl:            %s\n", info.Yoloctl)
	_, _ = fmt.Fprintf(w, "controller bundle: %s (protocol %d)\n", info.Bundle, info.ControllerProtocol)
	_, _ = fmt.Fprintf(w, "openshell-sandbox:  %s\n", info.OpenshellSandbox)
	if info.Limactl != "" {
		_, _ = fmt.Fprintf(w, "limactl:            %s\n", info.Limactl)
	} else {
		_, _ = fmt.Fprintln(w, "limactl:            (unavailable: not on PATH or --version failed)")
	}
	return nil
}

func writeVersionJSON(w io.Writer, info versionInfo) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(info)
}
