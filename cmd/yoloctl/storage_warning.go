package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"strconv"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/storagegc"
	vmhost "github.com/leogr/yoloctl/internal/vm"
)

var storageWarningDisk = storagegc.RootDisk

func diskReserveWarning() string {
	disk, err := storageWarningDisk()
	if err != nil || disk.Total == 0 || disk.Available > disk.Total || !disk.Low() {
		return ""
	}
	return fmt.Sprintf("VM system disk: %.1f GiB free; image preparation needs %.1f GiB; see yoloctl space storage to identify large boxes and free their caches",
		gib(disk.Available), gib(disk.TargetFree()))
}

// Run once per admitted worker, not once per image-warming/apply pass. Internal
// reads, completion and JSON commands keep their existing output.
func warnControllerDisk(req controller.Request) {
	switch req.Method {
	case "box.create", "box.recreate", "box.start", "box.list":
	default:
		return
	}
	if values := req.Flags["json"]; len(values) != 0 {
		asJSON, err := strconv.ParseBool(values[len(values)-1])
		if err != nil || asJSON {
			return
		}
	}
	if note := diskReserveWarning(); note != "" {
		cliWarn(logging.Storage, "%s", note)
	}
}

var diskWarningCall = callHostController

// Startup already proved the matching controller is running. This optional
// admin ping observes only statfs; failure never changes the lifecycle result.
func warnHostDisk(ctx context.Context, boot *spaceconfig.Bootstrap) bool {
	ctx, cancel := context.WithTimeout(ctx, vmhost.GuestProbeTimeout)
	defer cancel()
	var raw bytes.Buffer
	request := controller.Request{ID: rand.Text(), Method: "control.ping", Space: boot.Name,
		Flags: map[string][]string{controller.StorageWarningFlag: {"true"}}, Dash: -1}
	if err := diskWarningCall(ctx, boot, request, controller.ClientIO{Out: &raw, Err: io.Discard}); err != nil {
		return false
	}
	var note string
	if json.Unmarshal(raw.Bytes(), &note) == nil && note != "" {
		cliWarn(logging.Storage, "space %s: %s", boot.Name, note)
		return true
	}
	return false
}
