//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"errors"
	"sort"

	"github.com/steveyegge/beads/internal/memoryapi"
	storagememoryops "github.com/steveyegge/beads/internal/storage/memoryops"
	"github.com/steveyegge/beads/memoryops"
)

var _ memoryops.AtomicMemories = (*memories)(nil)

func (m *memories) Apply(ctx context.Context, req memoryops.BatchRequest) (memoryops.BatchResult, error) {
	for key, value := range req.Remember {
		if _, err := memoryapi.ResolveKey(key, value); err != nil || key == "" {
			return memoryops.BatchResult{}, errors.New("invalid memory batch write")
		}
	}
	for _, key := range req.Forget {
		if _, err := memoryapi.ValidateKey(key); err != nil {
			return memoryops.BatchResult{}, err
		}
		if _, exists := req.Remember[key]; exists {
			return memoryops.BatchResult{}, errors.New("memory batch cannot write and delete the same key")
		}
	}
	for key := range req.Expected {
		if _, err := memoryapi.ValidateKey(key); err != nil {
			return memoryops.BatchResult{}, err
		}
	}
	var result memoryops.BatchResult
	err := m.store.withConn(ctx, true, func(tx *sql.Tx) error {
		for key, expected := range req.Expected {
			value, _, err := storagememoryops.RecallInTx(ctx, tx, key)
			if err != nil {
				return err
			}
			if value != expected {
				return errors.New("memory generation changed; reread before publishing or pruning")
			}
		}
		keys := make([]string, 0, len(req.Remember))
		for key := range req.Remember {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value, _, err := storagememoryops.RecallInTx(ctx, tx, key)
			if err != nil {
				return err
			}
			if value == req.Remember[key] {
				continue
			}
			if _, err := storagememoryops.RememberInTx(ctx, tx, key, req.Remember[key]); err != nil {
				return err
			}
			result.Written++
		}
		for _, key := range req.Forget {
			_, found, err := storagememoryops.ForgetInTx(ctx, tx, key)
			if err != nil {
				return err
			}
			if found {
				result.Deleted++
			}
		}
		return nil
	})
	if err != nil {
		return memoryops.BatchResult{}, err
	}
	return result, nil
}
