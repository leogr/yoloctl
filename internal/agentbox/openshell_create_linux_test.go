package agentbox

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/openshell"
	"golang.org/x/crypto/ssh"
)

func TestNativeSDKCreationRefusesOrdinaryExecutorBeforeEffects(t *testing.T) {
	if err := (LiveApplier{}).createNativeContainer(t.Context(), &Config{}, "fixture", nil, openshell.QualifiedRelease{}, nil); err == nil {
		t.Fatal("ordinary executor reached native creation")
	}
}
func TestNativeStartupTargetPinsCreateCIDAndStartInstance(t *testing.T) {
	id := strings.Repeat("a", 64)
	d := &container.Details{ID: id, Running: true, Status: "running", PID: 42, StartedAt: "2026-10-31T00:00:00Z"}
	target, err := selectNativeStartedTarget(d, id, openshell.ActivationTarget{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := selectNativeStartedTarget(d, id, target); err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"id", "pid", "time", "stopped", "missing-time"} {
		t.Run(change, func(t *testing.T) {
			changed := *d
			switch change {
			case "id":
				changed.ID = strings.Repeat("b", 64)
			case "pid":
				changed.PID++
			case "time":
				changed.StartedAt = "2026-10-31T00:00:01Z"
			case "stopped":
				changed.Running = false
			case "missing-time":
				changed.StartedAt = ""
			}
			if _, err := selectNativeStartedTarget(&changed, id, target); err == nil {
				t.Fatal("replaced/invalid launch accepted")
			}
		})
	}
	if err := validateNativeCleanupState(d, id, target, true); err != nil {
		t.Fatal(err)
	}
	if err := validateNativeCleanupState(d, id, target, false); err == nil {
		t.Fatal("unstarted acknowledgement adopted running workload")
	}
	stopped := *d
	stopped.Running = false
	stopped.PID = 0
	stopped.Status = "exited"
	stopped.InactiveTaskFieldsObserved = true
	if err := validateNativeCleanupState(&stopped, id, target, true); err != nil {
		t.Fatal(err)
	}
	stopped.StartedAt = "2026-10-31T00:00:01Z"
	if err := validateNativeCleanupState(&stopped, id, target, true); err == nil {
		t.Fatal("cleanup adopted restarted instance")
	}
	created := &container.Details{ID: id, Status: "created", InactiveTaskFieldsObserved: true}
	if err := validateNativeCleanupState(created, id, openshell.ActivationTarget{}, false); err != nil {
		t.Fatal(err)
	}
	if err := validateNativeCleanupState(created, id, target, true); err != nil {
		t.Fatal("inactive missing task history incorrectly contradicted the known Start ACK", err)
	}
}
func startupSSHSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}
func TestNativeStartupSSHRequiresPinnedHandshakeAndOpensNoWorkload(t *testing.T) {
	for _, matches := range []bool{false, true} {
		t.Run(map[bool]string{false: "wrong-key", true: "pinned-key"}[matches], func(t *testing.T) {
			directory, err := os.MkdirTemp("/tmp", "os-startup-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(directory) })
			root, err := os.Open(directory)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = root.Close() }()
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(directory, "ssh.sock"), Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			signer := startupSSHSigner(t)
			expected := signer.PublicKey()
			if !matches {
				expected = startupSSHSigner(t).PublicKey()
			}
			config := &ssh.ServerConfig{NoClientAuth: true}
			config.AddHostKey(signer)
			var channels atomic.Int32
			var worker sync.WaitGroup
			worker.Add(1)
			go func() {
				defer worker.Done()
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer func() { _ = conn.Close() }()
				_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
				server, chans, requests, err := ssh.NewServerConn(conn, config)
				if err != nil {
					return
				}
				defer func() { _ = server.Close() }()
				go ssh.DiscardRequests(requests)
				for channel := range chans {
					channels.Add(1)
					_ = channel.Reject(ssh.Prohibited, "readiness opens no channels")
				}
			}()
			ready, err := probeNativeCreatedSSH(t.Context(), root, expected)
			_ = listener.Close()
			worker.Wait()
			if matches && (!ready || err != nil) {
				t.Fatal("pinned handshake refused", ready, err)
			}
			if !matches && (ready || !errors.Is(err, errNativeStartupHostKey)) {
				t.Fatal("wrong key accepted or hidden", ready, err)
			}
			if channels.Load() != 0 {
				t.Fatal("readiness opened workload/forward channel")
			}
		})
	}
}
func TestNativeStartupSSHDoesNotAcceptBannerOrDialAlone(t *testing.T) {
	directory, err := os.MkdirTemp("/tmp", "os-banner-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	root, err := os.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(directory, "ssh.sock"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		_, _ = conn.Write([]byte("SSH-2.0-fixture\r\n"))
		_ = conn.Close()
	}()
	ready, err := probeNativeCreatedSSH(t.Context(), root, startupSSHSigner(t).PublicKey())
	_ = listener.Close()
	<-done
	if ready || err != nil {
		t.Fatal("greeting substituted for handshake", ready, err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if ready, _ := probeNativeCreatedSSH(cancelled, root, startupSSHSigner(t).PublicKey()); ready {
		t.Fatal("cancelled probe became ready")
	}
}
