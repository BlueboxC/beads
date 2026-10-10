//go:build cgo

package main

import (
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage/graphstore"
)

// graphPreviewGoldenTypes maps each marker generation that a fresh workspace can
// be written with to the Type names a fresh init installs for it, in installation
// order. link-preview-v5 has no entry: it is the legacy generation (four
// installed Types, or six in a workspace made after the example Link Types were
// added), no bd writes it any more, and admission accepts it as it stands.
var graphPreviewGoldenTypes = map[string][]string{
	"link-preview-v6\n": {"memory", "issue", "dependency", "related", "example-follows", "example-cites"},
}

// An older bd checks only the marker (and a schema version that names the
// storage layout, which adding a Type does not change), so the marker is the one
// signal that a workspace holds a Type set the reader does not know. Changing
// what a fresh init installs without changing the marker lets an older bd open
// the workspace and fail later, at its first list or inventory read.
func TestGraphPreviewGenerationPinsFreshInstallTypes(t *testing.T) {
	golden, ok := graphPreviewGoldenTypes[graphPreviewGeneration]
	if !ok {
		t.Fatalf("graphPreviewGeneration is %q but graphPreviewGoldenTypes has no entry for it. "+
			"A new generation needs all three of: the new generation named in graphPreviewGeneration, "+
			"the old generation kept in the accepted set (graphPreviewSupportedGenerations) beside it, "+
			"and a golden entry here listing the Types it installs", graphPreviewGeneration)
	}
	if got := graphstore.FreshTypeNames(); !slices.Equal(got, golden) {
		t.Fatalf("a fresh init now installs %v, but generation %q pins %v. "+
			"Adding, removing or reordering an installed Type is a workspace format change: "+
			"bump graphPreviewGeneration to the next number, add that generation to the accepted set "+
			"(graphPreviewSupportedGenerations) while keeping the old one, and add a golden entry for it "+
			"here. Do not edit this entry in place", got, graphPreviewGeneration, golden)
	}
	if !graphPreviewGenerationSupported([]byte(graphPreviewGeneration)) {
		t.Fatalf("the generation a fresh init writes (%q) is not in the accepted set, so every new "+
			"workspace would be refused at its first command: add it to graphPreviewSupportedGenerations",
			graphPreviewGeneration)
	}
}
