package main

import (
	"context"
	"os"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/controller"
)

// saveVerifiedAccount publishes an account declaration after registering
// its exact postimage with the controller. Only a single-document account
// write is verifiable; a refused registration aborts the write.
func saveVerifiedAccount(ctx context.Context, c *account.Config) error {
	return c.SaveRecorded(func(data []byte) error {
		return controller.RecordDocument(ctx, controller.DocumentIntent{
			Kind: controller.DocumentAccount, Name: c.Name, Present: true, Data: data,
			UID: os.Geteuid(), GID: os.Getegid(), Mode: 0o600,
		})
	})
}
