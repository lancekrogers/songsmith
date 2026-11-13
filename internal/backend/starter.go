package backend

import "context"

// starterFunc adapts a function to Starter.
type starterFunc func(context.Context) error

func (f starterFunc) Start(ctx context.Context) error { return f(ctx) }

// StarterFunc wraps fn as a Starter.
func StarterFunc(fn func(context.Context) error) Starter { return starterFunc(fn) }
