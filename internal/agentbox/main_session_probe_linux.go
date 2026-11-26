package agentbox

import (
	"context"
	"net"
	"time"

	"github.com/leogr/yoloctl/internal/boxendpoint"
	"github.com/leogr/yoloctl/internal/sandbox"
	"golang.org/x/crypto/ssh"
)

// ProbeMainSession sends the request once on a fresh session channel of box
// name's endpoint and closes it: true when accepted, false when refused (an
// endpoint from before), an error when the endpoint could not be asked. It
// is the default PrivilegedMainSession implementation; nothing runs in the box.
func ProbeMainSession(ctx context.Context, name string) (bool, error) {
	return probeMainSessionAt(ctx, name, sandbox.SocketDirHost(name)+"/ssh.sock")
}

func probeMainSessionAt(ctx context.Context, name, socket string) (bool, error) {
	// The probe is one request and its reply. The caller ends it earlier.
	ctx, cancel := context.WithTimeout(ctx, runtimeStepFailSafe)
	defer cancel()
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", socket)
	if err != nil {
		return false, err
	}
	defer func() { _ = conn.Close() }()
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	defer stop()
	cc, chans, reqs, err := ssh.NewClientConn(conn, name, &ssh.ClientConfig{
		User:            "sandbox",
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	})
	if err != nil {
		return false, err
	}
	client := ssh.NewClient(cc, chans, reqs)
	defer func() { _ = client.Close() }()
	ch, chReqs, err := client.OpenChannel("session", nil)
	if err != nil {
		return false, err
	}
	go ssh.DiscardRequests(chReqs)
	defer func() { _ = ch.Close() }()
	return ch.SendRequest(boxendpoint.MainSessionRequest, true, nil)
}
