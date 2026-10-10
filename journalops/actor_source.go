package journalops

import "context"

type actorSourceKey struct{}
type actorOrigin struct{ actor, source string }

// WithActorSource records how a caller resolved its actor. It grants no authority.
// Bind the source to that exact actor so cascades with a different/system actor
// cannot inherit the initiating caller's provenance.
func WithActorSource(ctx context.Context, actor, source string) context.Context {
	switch source {
	case "flag", "env", "config", "git", "user", "unknown", "provided":
	default:
		source = "provided"
	}
	return context.WithValue(ctx, actorSourceKey{}, actorOrigin{actor, source})
}

// ActorSource returns resolution provenance, never authenticated identity.
// A named actor supplied without provenance is provided; an actorless row is empty.
func ActorSource(ctx context.Context, actor string) string {
	if actor == "" {
		return ""
	}
	if origin, ok := ctx.Value(actorSourceKey{}).(actorOrigin); ok && origin.actor == actor {
		return origin.source
	}
	return "provided"
}
