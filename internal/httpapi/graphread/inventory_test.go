package graphread

import (
	"errors"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/storage/graphstore"
)

func TestInventoryProjectionRefusesIncompleteContracts(t *testing.T) {
	for name, record := range map[string]any{
		"memory": graphstore.Record{Type: "https://example.test/types/missing"},
		"issue":  graphstore.IssueRecord{Type: "https://example.test/types/missing"},
		"link":   graphstore.LinkRecord{Type: "https://example.test/types/missing"},
		"other":  "not a supported record",
	} {
		t.Run(name, func(t *testing.T) {
			result, err := projectInventory(graphstore.Snapshot{WriterToken: "must-not-leak", Records: []any{record}})
			if !errors.Is(err, graphstore.ErrInvalidStore) || !reflect.DeepEqual(result, Inventory{}) {
				t.Fatalf("partial or silently filtered inventory: %+v, %v", result, err)
			}
		})
	}
}
