package issueops

import "context"

// ExternalResolver reports which requested cross-project prerequisites are
// satisfied. Missing keys are blocked. Implementations must only read providers;
// a provider's absence or unreadability must never be treated as completion.
// Independent databases do not provide a distributed transaction: the result is
// an observation at selection time, while local selection and claim remain atomic.
type ExternalResolver func(context.Context, []string) (map[string]bool, error)

type externalResolverKey struct{}

// WithExternalResolver binds provider resolution to one caller's context.
// Without a resolver, external prerequisites conservatively remain blocked.
func WithExternalResolver(ctx context.Context, resolver ExternalResolver) context.Context {
	return context.WithValue(ctx, externalResolverKey{}, resolver)
}

// ExternalResolverFromContext returns the caller's optional read-only resolver.
func ExternalResolverFromContext(ctx context.Context) ExternalResolver {
	resolver, _ := ctx.Value(externalResolverKey{}).(ExternalResolver)
	return resolver
}
