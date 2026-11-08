// Package principal carries the authenticated OIDC user of a request (E-74).
package principal

import "context"

type Principal struct {
	Sub   string
	Admin bool
}

type ctxKey struct{}

func With(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

func From(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok && p.Sub != ""
}
