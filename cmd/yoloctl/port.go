package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/port"
	"github.com/spf13/cobra"
)

func newPortCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "port",
		Short: "Forward ports from the host into a box",
		Long: `Manage published:target forwards for a box (Docker-Compose-style
naming: target = the box/container port, published = the host port). The
routes are:

  host:published  (forwarded by the space's VM adapter)
    -> VM loopback listener managed by the guest supervisor
      -> OpenShell SSH channel to workload 127.0.0.1:target

Legacy boxes retain their bridge/DNAT route and require the server to bind
0.0.0.0 (or its inner veth IP). Privileged boxes retain the direct bridge
route. host_ip and protocol default to 127.0.0.1 and tcp.`,
	}
	cmd.AddCommand(
		newPortForwardCmd(),
		newPortUnforwardCmd(),
		newPortListCmd(),
	)
	return cmd
}

func newPortForwardCmd() *cobra.Command {
	var spaceName string
	cmd := &cobra.Command{
		Use:   "forward BOX [PUBLISHED:]TARGET",
		Short: "Forward PUBLISHED:TARGET from the host into the box",
		Long: `Publishes a TCP port on the host's loopback interface and forwards
it to TARGET inside the box. New OpenShell boxes reach the workload's
127.0.0.1:TARGET through SSH; legacy and privileged boxes retain their
bridge routes. PUBLISHED is the host port and TARGET is the box/container port
(Docker-Compose-style naming); host_ip and protocol default to
127.0.0.1 and tcp.

  yoloctl port forward coder 3000:3000     # published 3000 -> target 3000
  yoloctl port forward coder 8080:80        # published 8080 -> target 80
  yoloctl port forward coder 3000           # shorthand for 3000:3000

The mapping survives ` + "`yoloctl port forward`" + ` exiting (the guest manages
the lifecycle). Use ` + "`yoloctl port list`" + ` to see managed forwards and
` + "`yoloctl port unforward`" + ` to tear one down. Forwards are also dropped
automatically on ` + "`yoloctl box stop`" + ` and ` + "`yoloctl box destroy`" + `.
A new OpenShell box's server may bind 127.0.0.1 or 0.0.0.0. Legacy and
privileged boxes require 0.0.0.0 (or the address reached by their bridge
route); a loopback-only server is unreachable on those routes.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := validateAgentboxName(name); err != nil {
				return err
			}
			published, target, err := port.ParseMapping(args[1])
			if err != nil {
				return asUserError(err)
			}
			if err := resolveSpaceFlag(cmd, &spaceName); err != nil {
				return err
			}
			pf := agentbox.Port{Published: published, Target: target}
			result, err := mutateAndReconcile(cmd.Context(), spaceName, name, func(cfg *agentbox.Config) error {
				for _, existing := range cfg.Ports {
					if existing.Published != pf.Published {
						continue
					}
					if existing == pf {
						// Idempotent: same mapping already declared.
						return nil
					}
					return asUserError(fmt.Errorf("published port %d is already declared in box %s with target port %d (use `port unforward` first or edit the YAML)",
						pf.Published, name, existing.Target))
				}
				cfg.Ports = append(cfg.Ports, pf)
				return nil
			})
			if err != nil {
				if errors.Is(err, port.ErrPortInUse) {
					return asUserError(err)
				}
				return err
			}
			if len(result.PortForwardsAdded) > 0 {
				cliInfo(logging.Box, "forwarded published:%d -> %s:%d", published, name, target)
			} else {
				cliInfo(logging.Box, "published:%d -> %s:%d already forwarded", published, name, target)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&spaceName, "space", defaultSpaceName, "Space hosting the box")
	return cmd
}

func newPortUnforwardCmd() *cobra.Command {
	var spaceName string
	cmd := &cobra.Command{
		Use:   "unforward BOX [PUBLISHED:]TARGET",
		Short: "Stop a single published:target forward",
		Long: `Stops the guest service for the named forward and drops it from
the box YAML. Idempotent: unforwarding a port that was not
declared is a no-op.

  yoloctl port unforward coder 3000:3000`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := validateAgentboxName(name); err != nil {
				return err
			}
			published, target, err := port.ParseMapping(args[1])
			if err != nil {
				return asUserError(err)
			}
			if err := resolveSpaceFlag(cmd, &spaceName); err != nil {
				return err
			}
			pf := agentbox.Port{Published: published, Target: target}
			found := false
			result, err := mutateAndReconcile(cmd.Context(), spaceName, name, func(cfg *agentbox.Config) error {
				kept := cfg.Ports[:0]
				for _, existing := range cfg.Ports {
					if existing == pf {
						found = true
						continue
					}
					kept = append(kept, existing)
				}
				cfg.Ports = kept
				return nil
			})
			if err != nil {
				return err
			}
			if !found && len(result.PortForwardsRemoved) == 0 {
				cliInfo(logging.Box, "no forward %d:%d on %s; nothing to do", published, target, name)
			} else {
				cliInfo(logging.Box, "unforwarded published:%d -> %s:%d", published, name, target)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&spaceName, "space", defaultSpaceName, "Space hosting the box")
	return cmd
}

func newPortListCmd() *cobra.Command {
	var (
		spaceName string
		asJSON    bool
	)
	cmd := &cobra.Command{
		Use:   "list [BOX]",
		Short: "List managed port forwards",
		Long: `Lists every yoloctl-managed port forward in the space. With
BOX, the list is filtered to that box's forwards. The matching VM supervisor
includes retained and restarting services; a listed service does not prove
that an application is listening inside the box.

  yoloctl port list                # every box in the space
  yoloctl port list coder
  yoloctl port list --json         # machine-readable output`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := resolveSpaceFlag(cmd, &spaceName); err != nil {
				return err
			}
			var filter string
			if len(args) == 1 {
				filter = args[0]
				if err := validateAgentboxName(filter); err != nil {
					return err
				}
			}
			c := container.New(spaceName)
			services, err := port.ListServices(cmd.Context(), c, filter)
			if err != nil {
				return err
			}
			if asJSON {
				return writePortsJSON(cmd.OutOrStdout(), services)
			}
			return writePortsTable(cmd.OutOrStdout(), services)
		},
	}
	cmd.Flags().StringVar(&spaceName, "space", defaultSpaceName, "Space hosting the boxes")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit JSON instead of the table view")
	return cmd
}

// writePortsTable renders managed services, including retained/restarting
// entries. It preserves the actual unit; presence is not application health.
func writePortsTable(w io.Writer, services []port.ForwardService) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "BOX\tHOST_IP\tPUBLISHED\tTARGET\tPROTOCOL\tUNIT")
	for _, service := range services {
		s := service.Spec
		p := agentbox.Port{Published: s.HostPort, Target: s.ContainerPort}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%s\t%s\n",
			s.Agentbox, p.ResolvedHostIP(), p.Published, p.Target, p.ResolvedProtocol(), service.Unit)
	}
	return tw.Flush()
}

// writePortsJSON emits a top-level array of forward objects in the resolved
// full form with snake_case field names.
func writePortsJSON(w io.Writer, services []port.ForwardService) error {
	type portJSON struct {
		Agentbox  string `json:"box"`
		HostIP    string `json:"host_ip"`
		Published int    `json:"published"`
		Target    int    `json:"target"`
		Protocol  string `json:"protocol"`
		Unit      string `json:"unit"`
	}
	out := make([]portJSON, 0, len(services))
	for _, service := range services {
		s := service.Spec
		p := agentbox.Port{Published: s.HostPort, Target: s.ContainerPort}
		out = append(out, portJSON{
			Agentbox:  s.Agentbox,
			HostIP:    p.ResolvedHostIP(),
			Published: p.Published,
			Target:    p.Target,
			Protocol:  p.ResolvedProtocol(),
			Unit:      service.Unit,
		})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
