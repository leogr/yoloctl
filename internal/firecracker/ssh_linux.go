//go:build linux && amd64

package firecracker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/sshtransport"
	"github.com/leogr/yoloctl/internal/vmwire"
	"golang.org/x/crypto/ssh"
)

// SSH uses only the selected VM's pinned host key and private client identity.
// It is invoked by the CLI's private exec transport, never by an installed daemon.
func SSH(ctx context.Context, name, workdir string, args []string, pty bool) error {
	dir, err := (Driver{}).InstanceDir(name)
	if err != nil {
		return err
	}
	state, err := readState(dir)
	if err != nil {
		return err
	}
	live, err := state.Helper.alive()
	if err != nil {
		return err
	}
	if !live {
		return errors.New("VM helper is stopped")
	}
	if err = checkHostStoragePause(ctx, state); err != nil {
		return err
	}
	config, err := guestClientConfig(dir)
	if err != nil {
		return err
	}
	conn, err := DialGuest(ctx, filepath.Join(state.Dir, "vsock"), vmwire.SSHPort)
	if err != nil {
		return err
	}
	return sshtransport.Run(ctx, conn, config, remoteCommand(workdir, args), pty, os.Stdin, os.Stdout, os.Stderr)
}

// dialGuestSSH is the helper's in-process connection to the VM it owns. It
// uses the same pinned identity and bounded handshake as SSH.
func dialGuestSSH(ctx context.Context, dir, runtime string) (*ssh.Client, error) {
	config, err := guestClientConfig(dir)
	if err != nil {
		return nil, err
	}
	conn, err := DialGuest(ctx, filepath.Join(runtime, "vsock"), vmwire.SSHPort)
	if err != nil {
		return nil, err
	}
	return sshtransport.Connect(ctx, conn, config)
}

func guestClientConfig(dir string) (*ssh.ClientConfig, error) {
	raw, err := safepath.ReadFile(dir, "id_ed25519", 16384)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		return nil, err
	}
	raw, err = safepath.ReadFile(dir, "known_hosts", 16384)
	if err != nil {
		return nil, err
	}
	marker, hosts, key, _, rest, err := ssh.ParseKnownHosts(raw)
	if err != nil {
		return nil, err
	}
	if marker != "" || len(hosts) != 1 || hosts[0] != "microvm" || len(strings.TrimSpace(string(rest))) != 0 {
		return nil, errors.New("invalid VM host identity")
	}
	return &ssh.ClientConfig{User: "yolo", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: ssh.FixedHostKey(key)}, nil
}

func remoteCommand(workdir string, args []string) string {
	tail := make([]string, len(args))
	for i, a := range args {
		tail[i] = quote(a)
	}
	command := strings.Join(tail, " ")
	if workdir != "" {
		if command == "" {
			command = "exec ${SHELL:-/bin/sh} -l"
		}
		command = "cd -- " + quote(workdir) + " && " + command
	}
	return command
}
