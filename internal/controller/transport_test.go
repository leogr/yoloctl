package controller

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestTransportCloseLetsBridgeExitOnEOF(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	marker := filepath.Join(t.TempDir(), "finished")
	cmd := exec.CommandContext(ctx, "sh", "-c", `printf ready; cat >/dev/null; sleep 0.05; printf finished >"$1"`, "bridge", marker)
	conn, err := StartTransport(cmd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := io.ReadFull(conn, make([]byte, len("ready"))); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(marker); err != nil || string(raw) != "finished" {
		t.Fatalf("bridge killed before graceful EOF cleanup: %q, %v", raw, err)
	}
	if cmd.ProcessState == nil || !cmd.ProcessState.Success() {
		t.Fatalf("bridge exit: %v", cmd.ProcessState)
	}
}

func TestTransportCloseBoundsUnresponsiveBridge(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", "printf ready; exec sleep 30")
	conn, err := StartTransport(cmd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := io.ReadFull(conn, make([]byte, len("ready"))); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != nil || cmd.ProcessState == nil || cmd.ProcessState.Success() {
		t.Fatalf("unresponsive bridge was not reaped promptly: %v, %v", ctx.Err(), cmd.ProcessState)
	}
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("read not unblocked by close")
	}
}
