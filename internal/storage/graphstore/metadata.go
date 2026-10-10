package graphstore

import (
	"encoding/json"
	"fmt"
	"sort"

	graph "github.com/steveyegge/beads/graphops"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	publicops "github.com/steveyegge/beads/issueops"
)

// commonMetadata admits one complete JSON object. The empty object is the
// public representation even when the native Issue plane stores no bytes.
func commonMetadata(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return json.RawMessage(`{}`), nil
	}
	canonical, err := graph.CanonicalizeJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: metadata: %v", storage.ErrValidation, err)
	}
	if len(canonical) == 0 || canonical[0] != '{' {
		return nil, fmt.Errorf("%w: metadata must be a JSON object", storage.ErrValidation)
	}
	return canonical, nil
}

func hasCommonMetadataPatch(p publicops.MetadataPatch) bool {
	return p.Replace.Set || p.Merge.Set || len(p.Set) > 0 || len(p.Unset) > 0
}

// Validate every supplied JSON value before the ordinary Issue merge can
// collapse duplicate object members. This keeps graph admission strict even
// when a later unset removes a set value from the resolved document.
func validateCommonMetadataPatch(p publicops.MetadataPatch) error {
	if err := issueops.ValidateMetadataPatch(p); err != nil {
		return err
	}
	if p.Replace.Set {
		if _, err := commonMetadata(p.Replace.Value); err != nil {
			return err
		}
	}
	if p.Merge.Set {
		if len(p.Merge.Value) == 0 {
			return fmt.Errorf("%w: metadata merge must be a JSON object", storage.ErrValidation)
		}
		if _, err := commonMetadata(p.Merge.Value); err != nil {
			return err
		}
	}
	keys := make([]string, 0, len(p.Set))
	for key := range p.Set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := p.Set[key]
		if _, err := graph.CanonicalizeJSON(value); err != nil {
			return fmt.Errorf("%w: metadata value for key %q: %v", storage.ErrValidation, key, err)
		}
	}
	return nil
}
