package main

import (
	"os"

	"github.com/leogr/yoloctl/internal/sandbox"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/spf13/cobra"
)

func newSSHProxyCmd() *cobra.Command {
	var spaceName string
	cmd := &cobra.Command{
		Use:    "ssh-proxy BOX",
		Short:  "Stdio bridge to a box's sandbox SSH socket (for ssh ProxyCommand)",
		Hidden: true, // internal plumbing: ssh(1) ProxyCommand, not a user command
		Long: "Reads from stdin and writes to stdout, bridging to the openshell-sandbox " +
			"Unix socket inside the box container through the VM adapter and " +
			"a guest socat bridge. Designed to be invoked as an ssh(1) " +
			"ProxyCommand by `yoloctl box exec` and other tools.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ab := args[0]
			if err := resolveSpaceFlag(cmd, &spaceName); err != nil {
				return err
			}
			if err := validateAgentboxName(ab); err != nil {
				return err
			}
			sh := vmhost.ShellCommand(cmd.Context(), spaceName,
				"sudo", "socat", "-", "UNIX-CONNECT:"+sandbox.SocketHostPath(ab))
			sh.Stdin = os.Stdin
			sh.Stdout = os.Stdout
			err := vmhost.RunWithFilteredStderr(sh, os.Stderr)
			// ssh terminates its ProxyCommand when the SSH session ends,
			// including successful execs. That signal cancels our bridge;
			// it is not a second, user-visible command failure.
			if cmd.Context().Err() != nil {
				return nil
			}
			return err
		},
	}
	cmd.Flags().StringVar(&spaceName, "space", defaultSpaceName, "Space hosting the box")
	return cmd
}
