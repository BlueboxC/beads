package journalops

import (
	"context"
	"testing"
)

func TestActorSourceDoesNotGrantOrLeakIdentity(t *testing.T) {
	ctx := WithActorSource(context.Background(), "Owner", "git")
	for _, tc := range []struct{ actor, source string }{{"Owner", "git"}, {"other", "provided"}, {"", ""}} {
		if got := ActorSource(ctx, tc.actor); got != tc.source {
			t.Errorf("%q source=%q, want %q", tc.actor, got, tc.source)
		}
	}
	if got := ActorSource(context.Background(), "agent"); got != "provided" {
		t.Fatal(got)
	}
	if got := ActorSource(WithActorSource(ctx, "agent", "authenticated-human"), "agent"); got != "provided" {
		t.Fatal(got)
	}
	if got := ActorSource(ctx, "Owner"); got != "git" {
		t.Fatal("derived context changed its parent")
	}
}
