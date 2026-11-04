package main

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"reflect"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/sandbox"
	"github.com/leogr/yoloctl/internal/sshtransport"
	"golang.org/x/crypto/ssh"
)

const managedChatConnectionTimeout = 5 * time.Second

var openManagedChatConnectionFn = openManagedChatConnection

// A connection pins one verified OpenShell peer. Neither feature probes nor
// later client commands redial a mutable box-name socket.
type managedChatConnection struct {
	Room                     *managedchat.Room
	Config                   *agentbox.Config
	store                    managedchat.Store
	client                   *ssh.Client
	containerID, incarnation string
	applied                  managedchat.Applied
	live                     map[string]managedchat.Connections // last verified status, per AIRC account
	stop                     func() bool
	ops                      managedChatConnectionOps
}

type managedChatConnectionOps struct {
	load    func(string, string) (*agentbox.Config, error)
	control func(context.Context, *managedchat.Room, *agentbox.Config) (managedchat.Control, *container.Details, error)
	dial    func(context.Context, *agentbox.Config, *container.Details) (net.Conn, error)
	active  func(context.Context, *container.Client, string) bool
}

func openManagedChatConnection(ctx context.Context, space string, store managedchat.Store) (*managedChatConnection, error) {
	return openManagedChatConnectionWith(ctx, space, store, managedChatConnectionOps{
		load: agentbox.Load, control: managedServerControl, dial: dialManagedChatPeer, active: holderUnitActiveFn,
	})
}

func openManagedChatConnectionWith(ctx context.Context, space string, store managedchat.Store, ops managedChatConnectionOps) (*managedChatConnection, error) {
	probe, cancel := context.WithTimeout(ctx, managedChatConnectionTimeout)
	defer cancel()
	room, err := store.ReadManifestSnapshot()
	if err != nil {
		return nil, err
	}
	cfg, err := ops.load(space, room.Server.Name)
	if err != nil {
		return nil, err
	}
	control, details, err := ops.control(probe, room, cfg)
	if err != nil {
		return nil, err
	}
	proof, err := store.Expected(room)
	if err != nil {
		return nil, err
	}
	applied, err := control.Call(probe, "status")
	if err != nil {
		return nil, err
	}
	if err := applied.Matches(proof); err != nil {
		return nil, err
	}
	conn, err := ops.dial(probe, cfg, details)
	if err != nil {
		return nil, err
	}
	client, err := sshtransport.Connect(probe, conn, &ssh.ClientConfig{User: "sandbox", HostKeyCallback: ssh.InsecureIgnoreHostKey()})
	if err != nil {
		return nil, err
	}
	result := &managedChatConnection{Room: room, Config: cfg, store: store, client: client,
		containerID: details.ID, incarnation: details.Labels[managedchat.IncarnationLabel], applied: applied, ops: ops}
	result.stop = context.AfterFunc(ctx, func() { _ = client.Close() })
	if err := result.Verify(probe); err != nil {
		_ = result.Close()
		return nil, err
	}
	return result, nil
}

func dialManagedChatPeer(ctx context.Context, cfg *agentbox.Config, details *container.Details) (net.Conn, error) {
	raw, err := (&net.Dialer{}).DialContext(ctx, "unix", sandbox.SocketHostPath(cfg.Name))
	if err != nil {
		return nil, err
	}
	conn, ok := raw.(*net.UnixConn)
	if !ok {
		_ = raw.Close()
		return nil, errors.New("managed chat SSH requires a native Unix peer")
	}
	// The pinned OpenShell supervisor binds this root-only socket before
	// spawning the unprivileged child (v0.0.72 ssh.rs:ssh_server_init).
	if err := verifyChatControlPeer(conn, details.ID, 0, 0); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

// Verify detects native or runtime replacement around setup/probing. A later
// lifecycle stop can disconnect this connection, but cannot redirect it.
func (c *managedChatConnection) Verify(ctx context.Context) error {
	probe, cancel := context.WithTimeout(ctx, managedChatConnectionTimeout)
	defer cancel()
	room, err := c.store.ReadManifestSnapshot()
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(c.Room, room) {
		return errors.New("managed chat identity or desired generation changed during setup")
	}
	cfg, err := c.ops.load(c.Config.Space, c.Config.Name)
	if err != nil {
		return err
	}
	// Setup's own validation refreshed c.Config's runtime chat projection;
	// the binds and identities it derives are re-verified by control below.
	if !agentbox.SameDeclaration(c.Config, cfg) {
		return errors.New("managed server declaration changed during setup")
	}
	control, details, err := c.ops.control(probe, room, cfg)
	if err != nil {
		return err
	}
	if details.ID != c.containerID || details.Labels[managedchat.IncarnationLabel] != c.incarnation {
		return errors.New("managed server container changed during setup")
	}
	proof, err := c.store.Expected(room)
	if err != nil {
		return err
	}
	applied, err := control.Call(probe, "status")
	if err != nil {
		return err
	}
	if err := applied.MatchesIncarnation(proof, c.applied); err != nil {
		return err
	}
	if !c.ops.active(probe, container.New(cfg.Space), cfg.Name) {
		return errors.New("managed server holder is not active; run chat ensure explicitly")
	}
	latest, err := c.store.ReadManifestSnapshot()
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(room, latest) {
		return errors.New("managed chat changed during server verification")
	}
	if err := probe.Err(); err != nil {
		return err
	}
	c.live = applied.Accounts
	return nil
}

// Connected reports whether the last verified server status counted a live
// connection of account. It is an observation, not a reservation.
func (c *managedChatConnection) Connected(account string) bool {
	return c.live[account].Connections > 0
}

func (c *managedChatConnection) Run(ctx context.Context, argv []string, in *os.File, out, notice io.Writer) error {
	if in == nil {
		var err error
		in, err = os.Open(os.DevNull)
		if err != nil {
			return err
		}
		defer func() { _ = in.Close() }()
	}
	return sshtransport.RunClient(ctx, c.client, remoteCommandString(argv), false, in, out, notice)
}

func (c *managedChatConnection) Close() error {
	if c.stop != nil {
		c.stop()
	}
	return c.client.Close()
}
