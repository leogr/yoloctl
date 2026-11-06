package main

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/auditmeta"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

var auditObserveLabels = func(ctx context.Context, space string) ([]container.AuditLabelObservation, error) {
	return container.New(space).AuditLabels(ctx)
}
var auditMetadataPath = layout.AuditMetadataPath

func publishAuditMetadata(ctx context.Context) error {
	c := spaceenv.Current()
	if c == nil {
		return errors.New("audit metadata requires the native controller")
	}
	release, err := auditAcquire(ctx, c.Space, false)
	if err != nil {
		return err
	}
	defer release()
	if err := (controller.Receipts{Dir: filepath.Join(c.DiskMount, ".controller", "receipts")}).CheckAuditReconcile(c.Space, c.DiskID); err != nil {
		return err
	}
	cfg, err := spaceconfig.Load(c.Space)
	if err != nil {
		return err
	}
	if cfg.Audit == nil || !cfg.Audit.Enabled {
		return nil
	}
	observed, err := auditObserveLabels(ctx, c.Space)
	if err != nil {
		return err
	}
	records := make([]auditmeta.Record, 0, len(observed))
	for _, row := range observed {
		if row.Kind != agentbox.KindAgentbox || row.Space != c.Space || row.Schema != "1" {
			continue
		}
		// Labels record launch context. A missing runtime privilege observation is
		// not evidence of confinement; a positively observed mismatch is unavailable.
		if row.Privileged != nil && (*row.Privileged != (row.RuntimeMode == string(agentbox.RuntimeModePrivileged))) {
			continue
		}
		if row.Network != "true" && row.Network != "false" {
			continue
		}
		mode, account := row.RuntimeMode, row.Account

		record := auditmeta.Record{ID: row.ID, Box: row.Box, Space: row.Space, Agent: row.Agent, Account: account, RuntimeMode: mode, NetworkUnconfined: row.Network == "true"}
		if record.Validate() != nil {
			continue
		}
		records = append(records, record)
	}
	raw, err := auditmeta.Encode(layout.AuditMetadataGeneration(c.Generation), time.Now(), records)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	path := auditMetadataPath()
	return safepath.WriteFile(filepath.Dir(path), filepath.Base(path), raw, 0600)
}

// maintainNativeAudit shares one admission with retention. A metadata failure
// must not prevent expiry, and two aligned background tickers must not compete.
func maintainNativeAudit(ctx context.Context, expiryDue bool) (bool, error) {
	var expiryErr error
	if expiryDue {
		expiryErr = pruneInactiveAudit(ctx)
	}
	attempt, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	metadataErr := publishAuditMetadata(attempt)
	return expiryDue && expiryErr == nil, errors.Join(expiryErr, metadataErr)
}
