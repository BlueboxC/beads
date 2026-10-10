//go:build cgo

package graphstore

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/graphpatch"
	"github.com/steveyegge/beads/internal/storage"
)

func TestMemoryPropertiesPatchModeExclusivity(t *testing.T) {
	patch, err := graphpatch.Parse([]byte(`[{"op":"replace","path":"/body","value":"patch"}]`))
	if err != nil {
		t.Fatal(err)
	}
	var store *Store // Ambiguous internal modes must refuse before any transaction.
	for _, mode := range []memoryWriteRequest{
		{hasTitle: true}, {hasBody: true}, {hasTitle: true, hasBody: true},
	} {
		mode.path, mode.propertiesPatch = "beads/plan", patch
		got, err := store.writeMemory(context.Background(), mode)
		if !errors.Is(err, storage.ErrValidation) || !reflect.ValueOf(got).IsZero() {
			t.Fatalf("ambiguous mode returned result: %+v %v", got, err)
		}
	}
}
