//go:build cgo

package embeddeddolt_test

import (
	"github.com/steveyegge/beads/backend/conformance"
	"testing"
)

func TestAtomicMemoryBatchGuardsAndNoopPreserveGeneration(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "mba")
	conformance.RunMemoriesAtomicBatchGuardedNoopAndPreservation(t, t.Context(), newEmbeddedMemoriesFixture(t, te, "mba"))
}
