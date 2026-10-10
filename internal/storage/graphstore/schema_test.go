package graphstore

import (
	"testing"

	graph "github.com/steveyegge/beads/graphops"
)

func TestInformationalDescriptorEndpointSemantics(t *testing.T) {
	const scope = "https://example.test/descriptor/"
	descriptor, err := relatedDescriptor(scope)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := graph.ParseTypeDescriptor(descriptor.CanonicalJSON())
	if err != nil {
		t.Fatal(err)
	}
	if parsed.ID() != scope+"types/preview-related-v2" {
		t.Fatal("corrected descriptor must have a fresh experimental identity")
	}
	source, hasSource := parsed.Source()
	target, hasTarget := parsed.Target()
	if !hasSource || !hasTarget {
		t.Fatal("Link descriptor must expose endpoint constraints")
	}
	for _, endpoint := range []graph.EndpointConstraint{source, target} {
		if required := endpoint.ConformsTo(); len(required) != 0 {
			t.Fatalf("conformsTo requires ALL listed Types, so a list of alternatives rejects mixed endpoints: %v", required)
		}
		if external, present := endpoint.External(); !present || external != graph.ExternalNone {
			t.Fatal("preview must refuse external endpoints")
		}
	}
	// The blocking adapter remains specifically constrained to Issue endpoints.
	blocking, err := dependencyDescriptor(scope)
	if err != nil {
		t.Fatal(err)
	}
	source, _ = blocking.Source()
	target, _ = blocking.Target()
	for _, endpoint := range []graph.EndpointConstraint{source, target} {
		required := endpoint.ConformsTo()
		if len(required) != 1 || required[0] != IssueTypeURL(scope) {
			t.Fatalf("blocking Dependency must still require Issue: %v", required)
		}
	}
}
