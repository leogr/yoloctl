package vmservice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"time"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/execpipe"
	"github.com/leogr/yoloctl/internal/vm"
	"github.com/leogr/yoloctl/internal/vmwire"
)

const serviceInventoryMaxBytes = 1 << 20

// InventoryFailSafe ends an inventory read that nothing else ends: it
// is reached from box attach's owned setup, which can outlive its client, and
// from detached post-stop checks. A client ends it earlier. Tests shorten it.
var InventoryFailSafe = 2 * time.Minute

var errServiceInventoryOverflow = errors.New("service inventory exceeds capture bound")

// Enforce the byte limit while exec copies stdout, not after an unrestricted
// Output/request buffer has been allocated. Do not embed bytes.Buffer: its
// promoted ReaderFrom would bypass Write during io.Copy.
type serviceInventoryCapture struct {
	data     []byte
	cancel   context.CancelFunc
	overflow bool
}

func (w *serviceInventoryCapture) Write(p []byte) (int, error) {
	if w.overflow {
		return 0, errServiceInventoryOverflow
	}
	n := len(p)
	if n > serviceInventoryMaxBytes-len(w.data) {
		n = serviceInventoryMaxBytes - len(w.data)
		w.data = append(w.data, p[:n]...)
		w.overflow = true
		w.cancel()
		return n, errServiceInventoryOverflow
	}
	w.data = append(w.data, p...)
	return n, nil
}

// Both selected service-manager commands use this bounded capture. This
// private read path changes no Stop semantics or legacy request transport.
func readServiceInventory(ctx context.Context, c *container.Client, native bool, prefix string, patterns ...string) (string, error) {
	read, cancel := context.WithTimeout(ctx, InventoryFailSafe)
	defer cancel()
	var cmd *exec.Cmd
	if native {
		raw, err := json.Marshal(vmwire.UnitRequest{Action: "inventory", Unit: Unit{Name: prefix}})
		if err != nil {
			return "", err
		}
		cmd = vm.ShellCommand(read, c.Env(), "sudo", "/sbin/yoloctl-init", "unit")
		cmd.Stdin = bytes.NewReader(raw)
	} else {
		args := append([]string{"sudo", "systemctl", "list-units", "--all", "--no-legend", "--plain", "--no-pager", "--full", "--type=service"}, patterns...)
		cmd = vm.ShellCommand(read, c.Env(), args...)
	}
	output := &serviceInventoryCapture{data: make([]byte, 0, serviceInventoryMaxBytes), cancel: cancel}
	// The read owns stdout, so the caller's cancellation and the fail-safe also
	// end the drain when an exited command leaves a descendant holding it.
	err := execpipe.Run(read, cmd, 0, output, nil)
	if output.overflow {
		err = errors.Join(err, errServiceInventoryOverflow)
	}
	if err = errors.Join(err, read.Err()); err != nil {
		return "", err
	}
	return string(output.data), nil
}
