package graphread

import (
	"context"
	"strings"

	"github.com/steveyegge/beads/internal/storage/graphstore"
)

// ScopeURL is the persisted authority identity, never a listener or Host value.
func (r *Reader) ScopeURL() string {
	if r == nil || r.store == nil {
		return ""
	}
	return r.store.ScopeURL()
}

// ValidateAuthority checks the current persisted binding and immutable installed
// contracts even when a caller would otherwise serve only retained cursor bytes.
// This does not detect a same-binding hot restore; that operation is unsupported
// while serving and must stop the authority process before restoring storage.
func (r *Reader) ValidateAuthority(ctx context.Context) error {
	if r == nil || r.store == nil {
		return graphstore.ErrInvalidStore
	}
	_, err := r.store.ReadType(ctx, strings.TrimPrefix(graphstore.MemoryTypeURL(r.store.ScopeURL()), r.store.ScopeURL()))
	return err
}
