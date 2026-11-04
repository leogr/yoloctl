package main

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/agentbox"
)

// missingAccountError is the strict-lifecycle failure for a box whose selected
// account manifest is missing: create/start/restart/recreate/reconcile
// never proceed with a silently unauthenticated, freshly initialized identity.
// Missing accounts, including a space default, are never auto-created.
func missingAccountError(cfg *agentbox.Config) error {
	return asUserError(fmt.Errorf(
		"box %q selects account %q, which does not exist — refusing to start it with a fresh, unauthenticated identity.\n"+
			"  create the account:   yoloctl account create %s --space %s\n"+
			"  or re-point the box:  yoloctl box edit %s --space %s",
		cfg.Name, cfg.Account, cfg.Account, cfg.Space, cfg.Name, cfg.Space))
}

// requireAccountForLifecycle loads a valid identity under its account lock.
// Native directory preparation happens later inside the VM. The surrounding
// space operation lock excludes destroy through completion of runtime setup.
func requireAccountForLifecycle(cfg *agentbox.Config) error {
	if !cfg.HasRealAccount() {
		return nil
	}
	release, err := account.Lock(cfg.Space, cfg.Account)
	if err != nil {
		return err
	}
	defer release()
	if _, err := account.Load(cfg.Space, cfg.Account); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return missingAccountError(cfg)
		}
		return err
	}

	return nil
}
