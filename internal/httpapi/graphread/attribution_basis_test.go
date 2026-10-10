package graphread

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi/bdpwire"
	"github.com/steveyegge/beads/internal/storage/graphstore"
)

func TestProjectAttributionUsesPublicBasisWithoutChangingStoredStatus(t *testing.T) {
	stored := graphstore.Attribution{Actor: "agent:writer", Status: "claimed"}
	public, err := projectAttribution(stored)
	if err != nil {
		t.Fatal(err)
	}
	if public.Principal != stored.Actor || public.Basis != bdpwire.AttributionWriterSupplied || stored.Status != "claimed" {
		t.Fatalf("stored=%+v public=%+v", stored, public)
	}
	unknown, err := projectAttribution(graphstore.Attribution{Actor: "agent:imported", Status: "unknown"})
	if err != nil || unknown == nil || unknown.Principal != "agent:imported" || unknown.Basis != bdpwire.AttributionUnknown {
		t.Fatalf("imported attribution basis: %v %+v", err, unknown)
	}
	absent, err := projectAttribution(graphstore.Attribution{Status: "unknown"})
	if err != nil || absent != nil {
		t.Fatalf("principal-free attribution should be absent: %v %+v", err, absent)
	}
	absent, err = projectAttribution(graphstore.Attribution{})
	if err != nil || absent != nil {
		t.Fatalf("zero attribution should be absent: %v %+v", err, absent)
	}
	for _, invalid := range []graphstore.Attribution{{Actor: "agent:writer", Status: "verified"}, {Status: "claimed"}} {
		if projected, err := projectAttribution(invalid); projected != nil || !errors.Is(err, graphstore.ErrInvalidStore) {
			t.Fatalf("invalid stored attribution %+v must fail closed: %+v, %v", invalid, projected, err)
		}
	}
}
