package agentbox

import (
	"context"
	"errors"
	"fmt"

	"github.com/leogr/yoloctl/internal/spaceenv"
)

type recreateCacheClearKey struct{}
type recreateCacheClear struct{ report func(uint64) }

// WithRecreateCacheClear carries only explicit recreate command intent. It is
// not runtime authority: Create still requires a fresh complete absence read
// after lifecycle removal, and the cache store checks its ownership separately.
func WithRecreateCacheClear(ctx context.Context, report func(uint64)) context.Context {
	return context.WithValue(ctx, recreateCacheClearKey{}, recreateCacheClear{report: report})
}

func RecreateCacheClearRequested(ctx context.Context) bool {
	_, ok := ctx.Value(recreateCacheClearKey{}).(recreateCacheClear)
	return ok
}

func clearReplacementCache(ctx context.Context, name string, absent func(context.Context, string) error, clear func(context.Context, string) (uint64, error)) error {
	intent, ok := ctx.Value(recreateCacheClearKey{}).(recreateCacheClear)
	if !ok {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := absent(ctx, name); err != nil {
		return fmt.Errorf("cache retained before replacement: %w", err)
	}
	bytes, err := clear(ctx, name)
	if err != nil {
		return fmt.Errorf("clear replacement cache: %w", err)
	}
	if intent.report != nil {
		intent.report(bytes)
	}
	return nil
}

func (a LiveApplier) clearCacheBeforeReplacementCreate(ctx context.Context, c *Config) error {
	return clearReplacementCache(ctx, c.Name,
		func(check context.Context, name string) error {
			if !spaceenv.Native() || a.Client == nil || a.VerifyStorage == nil {
				return errors.New("replacement cache clear requires admitted native infrastructure")
			}
			if err := spaceenv.RequireSpace(c.Space); err != nil {
				return err
			}
			if err := a.VerifyStorage(check); err != nil {
				return err
			}
			rows, err := a.Client.List(check, "")
			if err != nil {
				return err
			}
			for _, row := range rows {
				if row.Name == "" {
					return errors.New("container inventory has an unnamed entry")
				}
				if row.Name == name {
					return errors.New("old container has not been removed")
				}
			}
			return nil
		},
		func(check context.Context, name string) (uint64, error) {
			return ClearCacheRuntime(check, c)
		})
}
