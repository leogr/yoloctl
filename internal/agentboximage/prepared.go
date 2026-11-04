package agentboximage

import "context"

type preparedImageKey struct{}
type PreparedImageFunc func(context.Context, PreparedContext) (string, bool, error)

// WithPreparedImage installs only trusted worker state, never public argv.
func WithPreparedImage(ctx context.Context, f PreparedImageFunc) context.Context {
	return context.WithValue(ctx, preparedImageKey{}, f)
}
func preparedImage(ctx context.Context, c Client, base string, candidates []string, opts Options) (string, bool, error) {
	f, _ := ctx.Value(preparedImageKey{}).(PreparedImageFunc)
	if f == nil {
		return "", false, nil
	}
	p, err := PrepareComposed(ctx, base, candidates, opts)
	if err != nil {
		return "", true, err
	}
	return f(ctx, p)
}
