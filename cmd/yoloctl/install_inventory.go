package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/leogr/yoloctl/internal/installmigration"
	"github.com/leogr/yoloctl/internal/spaceconfig"
)

// consentSnapshot is a space's identity and state as the read-only install
// inventory observed it; two observations that differ mean the space changed
// during the check.
func consentSnapshot(boot *spaceconfig.Bootstrap, state string) (installmigration.Snapshot, error) {
	if boot.Initializing || boot.Import != nil || boot.ImageUpdate != nil {
		return installmigration.Snapshot{}, errors.New("space requires initialization/import/image recovery")
	}
	raw, err := json.Marshal(boot)
	if err != nil {
		return installmigration.Snapshot{}, err
	}
	s := installmigration.Snapshot{Space: boot.Name, Bundle: boot.Bundle, State: state, DiskID: boot.DiskID, DiskUUID: boot.DiskUUID, Generation: boot.Generation, Bootstrap: fmt.Sprintf("%x", sha256.Sum256(raw))}
	return s, s.Validate()
}

// Tests substitute observations, never a production environment escape.
var observeInstallConsent = readInstallConsent

func readInstallConsent(ctx context.Context, space string) (installmigration.Snapshot, error) {
	boot, err := spaceconfig.LoadBootstrap(space)
	if err != nil {
		return installmigration.Snapshot{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	inst, found, err := findSpaceInstance(ctx, space)
	if err != nil {
		return installmigration.Snapshot{}, err
	}
	if !found {
		return installmigration.Snapshot{}, errors.New("VM is missing")
	}
	return consentSnapshot(boot, inst.Status)
}
