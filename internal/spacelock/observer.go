package spacelock

import "context"

type Event struct {
	Space, Token    string
	Exclusive, Held bool
}
type Observer func(context.Context, Event)
type observerKey struct{}

func WithObserver(ctx context.Context, observe Observer) context.Context {
	return context.WithValue(ctx, observerKey{}, observe)
}
func observe(ctx context.Context, event Event) {
	if fn, ok := ctx.Value(observerKey{}).(Observer); ok {
		fn(ctx, event)
	}
}
