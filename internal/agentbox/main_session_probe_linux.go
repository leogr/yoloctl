package agentbox

import (
	"context"
	"net"
	"time"

	"github.com/leogr/yoloctl/internal/sandbox"
	"golang.org/x/crypto/ssh"
)

// MainSessionRequest is the privileged endpoint's marking request (contract
// M-b section 7): empty, reply required, on a session channel, before any
// shell or exec. An accepted mark alone starts and stops nothing.
const MainSessionRequest = "yoloctl-main-session"

const mainSessionProbeTimeout = 10 * time.Second

// ProbeMainSession sends the request once on a fresh session channel of box
// name's endpoint and closes it: true when accepted, false when refused (an
// endpoint from before), an error when the endpoint could not be asked. It
// is PrivilegedMainSession's implementation once the endpoint serves the
// request; nothing runs in the box.
func ProbeMainSession(ctx context.Context, name string) (bool, error) {
	return probeMainSessionAt(ctx, name, sandbox.SocketDirHost(name)+"/ssh.sock")
}

func probeMainSessionAt(ctx context.Context, name, socket string) (bool, error) {
	dialer := net.Dialer{Timeout: mainSessionProbeTimeout}
	conn, err := dialer.DialContext(ctx, "unix", socket)
	if err != nil {
		return false, err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(mainSessionProbeTimeout))
	cc, chans, reqs, err := ssh.NewClientConn(conn, name, &ssh.ClientConfig{
		User:            "sandbox",
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         mainSessionProbeTimeout,
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
	return ch.SendRequest(MainSessionRequest, true, nil)
}
