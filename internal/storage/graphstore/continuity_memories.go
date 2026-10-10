package graphstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/steveyegge/beads/graphops"
	"github.com/steveyegge/beads/internal/memoryapi"
	"github.com/steveyegge/beads/internal/storage"
	txmemory "github.com/steveyegge/beads/internal/storage/memoryops"
	"github.com/steveyegge/beads/memoryops"
)

const continuityPointer = "graph-continuity:v1:"

// ContinuityKeyMetadata binds a canonical Memory to its exact keyed identity.
const ContinuityKeyMetadata = "forkContinuityKey"

// ContinuityMemories adapts the fork's keyed plane without copying Memory bodies.
// Human knowledge is retained by Graph Preview; derived catalogs and observed
// activity stay regenerable config rows in this same database.
type ContinuityMemories struct {
	store *Store
	actor string
}

func (s *Store) Memories(actor string) *ContinuityMemories {
	return &ContinuityMemories{store: s, actor: actor}
}

func derivedContinuityKey(key string) bool {
	return strings.HasPrefix(key, "@knowledge/code/") || key == "@knowledge/catalog" || strings.HasPrefix(key, "@knowledge/catalog-part/") || strings.HasPrefix(key, "@activity/")
}

func (m *ContinuityMemories) transaction(ctx context.Context, write bool, fn func(*sql.Tx) error) error {
	return m.store.withTx(ctx, write, func(tx *sql.Tx) error {
		if err := checkBinding(ctx, tx, m.store.options); err != nil {
			return err
		}
		return fn(tx)
	})
}

func (m *ContinuityMemories) resolve(ctx context.Context, tx *sql.Tx, key string) (string, bool, error) {
	raw, found, err := txmemory.RecallInTx(ctx, tx, key)
	if err != nil || !found || derivedContinuityKey(key) {
		return raw, found, err
	}
	path, ok := strings.CutPrefix(raw, continuityPointer)
	if !ok {
		return "", false, fmt.Errorf("%w: continuity key %q has no canonical pointer", ErrInvalidStore, key)
	}
	record, err := m.store.showMemoryInTx(ctx, tx, path)
	if err != nil {
		return "", false, fmt.Errorf("%w: continuity key %q: %w", ErrInvalidStore, key, err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(record.Metadata, &metadata); err != nil || metadata[ContinuityKeyMetadata] != key {
		return "", false, fmt.Errorf("%w: continuity key and Memory metadata disagree", ErrInvalidStore)
	}
	return record.Properties.Body, true, nil
}

func (m *ContinuityMemories) Remember(ctx context.Context, req memoryops.RememberRequest) (memoryops.RememberResult, error) {
	key, err := memoryapi.ResolveKey(req.Key, req.Content)
	if err != nil {
		return memoryops.RememberResult{}, err
	}
	if !utf8.ValidString(key) || !utf8.ValidString(req.Content) || !utf8.ValidString(m.actor) {
		return memoryops.RememberResult{}, fmt.Errorf("%w: continuity requires UTF-8", memoryops.ErrValidation)
	}
	result := memoryops.RememberResult{Key: key, Value: req.Content}
	err = m.transaction(ctx, true, func(tx *sql.Tx) error {
		_, found, err := m.resolve(ctx, tx, key)
		if err != nil {
			return err
		}
		result.Replaced = found
		return m.rememberInTx(ctx, tx, key, req.Content)
	})
	if err != nil {
		return memoryops.RememberResult{}, err
	}
	return result, nil
}

func (m *ContinuityMemories) rememberInTx(ctx context.Context, tx *sql.Tx, key, value string) error {
	if derivedContinuityKey(key) {
		if err := m.store.touchCoordination(ctx, tx); err != nil {
			return err
		}
		_, err := txmemory.RememberInTx(ctx, tx, key, value)
		if err != nil {
			return err
		}
		return m.store.afterStage("continuity-derived")
	}
	raw, found, err := txmemory.RecallInTx(ctx, tx, key)
	if err != nil {
		return err
	}
	if found {
		path, ok := strings.CutPrefix(raw, continuityPointer)
		if !ok {
			return fmt.Errorf("%w: missing continuity pointer", ErrInvalidStore)
		}
		current, err := m.store.showMemoryInTx(ctx, tx, path)
		if err != nil {
			return err
		}
		if _, _, err := m.resolve(ctx, tx, key); err != nil {
			return err
		}
		_, err = m.store.writeMemoryInTx(ctx, tx, memoryWriteRequest{path: path, actor: m.actor, expectedRevision: current.Revision, hasBody: true, body: value})
		if err != nil {
			return err
		}
		// Basic Remember still writes on equal values without minting history.
		if err := m.store.touchCoordination(ctx, tx); err != nil {
			return err
		}
		if _, err := txmemory.RememberInTx(ctx, tx, key, raw); err != nil {
			return err
		}
		return m.store.afterStage("continuity-pointer")
	}
	token, err := freshToken()
	if err != nil {
		return err
	}
	path := "beads/continuity-" + token
	metadata, err := canonicalJSON(map[string]string{ContinuityKeyMetadata: key})
	if err != nil {
		return err
	}
	record := Record{ID: graphops.CanonicalURL(m.store.ScopeURL(), path), Type: MemoryTypeURL(m.store.ScopeURL()), Revision: token, Version: token, Properties: Properties{Title: key, Body: value}, Metadata: metadata, Owned: []json.RawMessage{}, Attribution: Attribution{Actor: m.actor, Status: "claimed", RecordedAt: time.Now().UTC().Format(time.RFC3339Nano)}}
	if m.actor == "" {
		record.Attribution.Status = "unknown"
	}
	properties, err := canonicalJSON(record.Properties)
	if err != nil {
		return err
	}
	snapshot, err := canonicalJSON(record)
	if err != nil {
		return err
	}
	if err := m.store.createInTx(ctx, tx, CreateRequest{Path: path, Actor: m.actor}, record, properties, snapshot); err != nil {
		return err
	}
	if _, err := txmemory.RememberInTx(ctx, tx, key, continuityPointer+path); err != nil {
		return err
	}
	return m.store.afterStage("continuity-pointer")
}

func (m *ContinuityMemories) Recall(ctx context.Context, req memoryops.RecallRequest) (memoryops.RecallResult, error) {
	key, err := memoryapi.ValidateKey(req.Key)
	if err != nil {
		return memoryops.RecallResult{}, err
	}
	result := memoryops.RecallResult{Key: key}
	err = m.transaction(ctx, false, func(tx *sql.Tx) error {
		var err error
		result.Value, result.Found, err = m.resolve(ctx, tx, key)
		return err
	})
	if err != nil {
		return memoryops.RecallResult{}, err
	}
	return result, nil
}

func (m *ContinuityMemories) Forget(ctx context.Context, req memoryops.ForgetRequest) (memoryops.ForgetResult, error) {
	key, err := memoryapi.ValidateKey(req.Key)
	if err != nil {
		return memoryops.ForgetResult{}, err
	}
	result := memoryops.ForgetResult{Key: key}
	err = m.transaction(ctx, true, func(tx *sql.Tx) error {
		var err error
		result.Value, result.Found, err = m.resolve(ctx, tx, key)
		if err != nil || !result.Found {
			return err
		}
		return m.forgetInTx(ctx, tx, key)
	})
	if err != nil {
		return memoryops.ForgetResult{}, err
	}
	return result, nil
}

func (m *ContinuityMemories) forgetInTx(ctx context.Context, tx *sql.Tx, key string) error {
	raw, found, err := txmemory.RecallInTx(ctx, tx, key)
	if err != nil || !found {
		return err
	}
	if !derivedContinuityKey(key) {
		path, ok := strings.CutPrefix(raw, continuityPointer)
		if !ok {
			return fmt.Errorf("%w: missing continuity pointer", ErrInvalidStore)
		}
		record, err := m.store.showMemoryInTx(ctx, tx, path)
		if err != nil {
			return err
		}
		_, err = m.store.deleteMemoryInTx(ctx, tx, MemoryDeleteRequest{Path: path, Actor: m.actor, ExpectedRevision: record.Revision})
		return err
	}
	if err := m.store.touchCoordination(ctx, tx); err != nil {
		return err
	}
	_, _, err = txmemory.ForgetInTx(ctx, tx, key)
	return err
}

func (s *Store) removeContinuityPointerInTx(ctx context.Context, tx *sql.Tx, path string, record Record) error {
	var metadata map[string]any
	if err := json.Unmarshal(record.Metadata, &metadata); err != nil {
		return err
	}
	key, ok := metadata[ContinuityKeyMetadata].(string)
	if !ok {
		return nil
	}
	raw, found, err := txmemory.RecallInTx(ctx, tx, key)
	if err != nil {
		return err
	}
	if !found || raw != continuityPointer+path {
		return fmt.Errorf("%w: deletion would orphan a continuity key", ErrInvalidStore)
	}
	_, _, err = txmemory.ForgetInTx(ctx, tx, key)
	return err
}

func (m *ContinuityMemories) List(ctx context.Context, req memoryops.ListRequest) (memoryops.ListResult, error) {
	result := memoryops.ListResult{Memories: map[string]string{}}
	err := m.transaction(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = m.listInTx(ctx, tx, req)
		return err
	})
	if err != nil {
		return memoryops.ListResult{}, err
	}
	return result, nil
}

func (m *ContinuityMemories) Apply(ctx context.Context, req memoryops.BatchRequest) (memoryops.BatchResult, error) {
	if !utf8.ValidString(m.actor) {
		return memoryops.BatchResult{}, fmt.Errorf("%w: continuity actor requires UTF-8", memoryops.ErrValidation)
	}
	for key, value := range req.Remember {
		if _, err := memoryapi.ValidateKey(key); err != nil {
			return memoryops.BatchResult{}, err
		}
		if err := memoryapi.ValidateContent(value); err != nil {
			return memoryops.BatchResult{}, err
		}
		if !utf8.ValidString(key) || !utf8.ValidString(value) {
			return memoryops.BatchResult{}, fmt.Errorf("%w: continuity requires UTF-8", memoryops.ErrValidation)
		}
	}
	for _, key := range req.Forget {
		if _, err := memoryapi.ValidateKey(key); err != nil {
			return memoryops.BatchResult{}, err
		}
		if _, ok := req.Remember[key]; ok {
			return memoryops.BatchResult{}, fmt.Errorf("%w: remember and forget overlap", memoryops.ErrValidation)
		}
	}
	result := memoryops.BatchResult{}
	err := m.transaction(ctx, true, func(tx *sql.Tx) error {
		for key, expected := range req.Expected {
			actual, found, err := m.resolve(ctx, tx, key)
			if err != nil {
				return err
			}
			if actual != expected || (expected == "" && found) {
				return fmt.Errorf("%w: continuity guard differs for %q", ErrConflict, key)
			}
		}
		keys := make([]string, 0, len(req.Remember))
		for key := range req.Remember {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value, found, err := m.resolve(ctx, tx, key)
			if err != nil {
				return err
			}
			if found && value == req.Remember[key] {
				continue
			}
			if err := m.rememberInTx(ctx, tx, key, req.Remember[key]); err != nil {
				return err
			}
			result.Written++
		}
		deleted := map[string]bool{}
		for _, key := range req.Forget {
			if deleted[key] {
				continue
			}
			deleted[key] = true
			_, found, err := m.resolve(ctx, tx, key)
			if err != nil {
				return err
			}
			if !found {
				continue
			}
			if err := m.forgetInTx(ctx, tx, key); err != nil {
				return err
			}
			result.Deleted++
		}
		return nil
	})
	if err != nil {
		return memoryops.BatchResult{}, err
	}
	return result, nil
}

var _ memoryops.Memories = (*ContinuityMemories)(nil)
var _ memoryops.AtomicMemories = (*ContinuityMemories)(nil)

func (m *ContinuityMemories) listInTx(ctx context.Context, tx *sql.Tx, req memoryops.ListRequest) (memoryops.ListResult, error) {
	result := memoryops.ListResult{Memories: map[string]string{}}
	if err := checkCurrentReadBytes(ctx, tx); err != nil {
		return result, err
	}
	var count uint64
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM graph_preview_catalog WHERE allocation_state='live'").Scan(&count); err != nil {
		return result, err
	}
	if count > PreviewSnapshotLimit {
		return result, fmt.Errorf("%w: continuity exceeds %d live Resources", ErrLimitExceeded, PreviewSnapshotLimit)
	}
	if err := checkContinuityReadBounds(ctx, tx); err != nil {
		return result, err
	}
	rows, err := txmemory.ListInTx(ctx, tx, req)
	if err != nil {
		return result, err
	}
	for key, raw := range rows {
		if derivedContinuityKey(key) {
			result.Memories[key] = raw
			continue
		}
		value, _, err := m.resolve(ctx, tx, key)
		if err != nil {
			return result, err
		}
		result.Memories[key] = value
	}
	result.Memories = memoryapi.FilterMemories(result.Memories, req.Search)
	return result, nil
}

// ReadContinuity joins the canonical graph and fork projections in one snapshot.
func (s *Store) ReadContinuity(ctx context.Context) (memoryops.ListResult, Snapshot, error) {
	var plane memoryops.ListResult
	var snapshot Snapshot
	err := s.withTx(ctx, false, func(tx *sql.Tx) error {
		var err error
		snapshot, err = s.currentSnapshotInTx(ctx, tx)
		if err != nil {
			return err
		}
		if err := checkContinuityReadBounds(ctx, tx); err != nil {
			return err
		}
		rows, err := txmemory.ListInTx(ctx, tx, memoryops.ListRequest{})
		if err != nil {
			return err
		}
		canonical := map[string]Record{}
		for _, entry := range snapshot.Records {
			if record, ok := entry.(Record); ok {
				canonical[record.ID] = record
			}
		}
		plane.Memories = make(map[string]string, len(rows))
		for key, raw := range rows {
			if derivedContinuityKey(key) {
				plane.Memories[key] = raw
				continue
			}
			path, ok := strings.CutPrefix(raw, continuityPointer)
			record, found := canonical[graphops.CanonicalURL(s.ScopeURL(), path)]
			if !ok || !found || continuityKey(record.Metadata) != key {
				return fmt.Errorf("%w: continuity pointer lacks its canonical Memory", ErrInvalidStore)
			}
			plane.Memories[key] = record.Properties.Body
		}
		return nil
	})
	if err != nil {
		return memoryops.ListResult{}, Snapshot{}, err
	}
	return plane, snapshot, nil
}

func continuityKey(metadata json.RawMessage) string {
	var fields map[string]any
	if json.Unmarshal(metadata, &fields) != nil {
		return ""
	}
	key, _ := fields[ContinuityKeyMetadata].(string)
	return key
}
func preserveContinuityKey(previous, next json.RawMessage) error {
	var before, after map[string]json.RawMessage
	_ = json.Unmarshal(previous, &before)
	_ = json.Unmarshal(next, &after)
	old, oldFound := before[ContinuityKeyMetadata]
	value, found := after[ContinuityKeyMetadata]
	if oldFound != found || !bytes.Equal(old, value) {
		return fmt.Errorf("%w: %s is managed by the continuity adapter", storage.ErrValidation, ContinuityKeyMetadata)
	}
	return nil
}
