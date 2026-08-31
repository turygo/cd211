// Package authn carries the verified authentication result across HTTP
// boundaries without exposing credentials to downstream handlers or logging.
package authn

import "context"

type PrincipalKind string

const (
	OperatorPrincipal     PrincipalKind = "operator"
	QBTClientPrincipal    PrincipalKind = "qbt_client"
	NativeClientPrincipal PrincipalKind = "native_client"
)

type Method string

const (
	BasicMethod       Method = "basic"
	SessionMethod     Method = "session"
	QBTKeyMethod      Method = "qbt_key"
	NativeTokenMethod Method = "native_token"
)

type Principal struct {
	Kind   PrincipalKind
	Method Method
}

type contextKey struct{}

func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, contextKey{}, principal)
}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(contextKey{}).(Principal)
	return principal, ok
}
