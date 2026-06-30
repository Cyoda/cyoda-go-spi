package spi

import "context"

type uniqueKeysCtxKey struct{}

// WithUniqueKeys attaches the declared []UniqueKey for the current store to ctx,
// making them available to CompositeUniqueKeyCapable implementations via UniqueKeysFromContext.
func WithUniqueKeys(ctx context.Context, keys []UniqueKey) context.Context {
	return context.WithValue(ctx, uniqueKeysCtxKey{}, keys)
}

// UniqueKeysFromContext retrieves the []UniqueKey stored by WithUniqueKeys.
// Returns nil when no keys were attached to ctx.
func UniqueKeysFromContext(ctx context.Context) []UniqueKey {
	if v, ok := ctx.Value(uniqueKeysCtxKey{}).([]UniqueKey); ok {
		return v
	}
	return nil
}
