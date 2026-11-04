//go:build linux

package agentbox

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/boxendpoint"
)

// The readiness probe used by create, start and restart accepts the
// endpoint's greeting.
func TestReadinessProbeAcceptsEndpoint(t *testing.T) {
	dir := t.TempDir()
	srv, err := boxendpoint.NewServer(boxendpoint.Config{Workdir: dir, Env: []string{"HOME=" + dir}})
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "ssh.sock")
	ln, err := boxendpoint.ListenPrivate(sock)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Shutdown([]net.Listener{ln}, time.Second)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := probeSandboxSSH(ctx, sock); err != nil {
		t.Fatal(err)
	}
}
