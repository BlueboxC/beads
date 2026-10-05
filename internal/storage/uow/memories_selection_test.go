//go:build cgo

package uow

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/memoryapi"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/domain/db"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	storagememoryops "github.com/steveyegge/beads/internal/storage/memoryops"
	"github.com/steveyegge/beads/memoryops"
)

// This exercises the UOW config use case and SQL repository against real Dolt.
// The observation counts key/value bytes returned by the repository, not wire
// overhead, engine page reads or allocations. Server lifecycle is covered by
// the existing UOW conformance fixture.
func TestMemoriesListSelectionBoundsTransferredValues(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	bootstrap, closeBootstrap, err := embeddeddolt.OpenSQL(ctx, dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeBootstrap() })
	if _, err := bootstrap.ExecContext(ctx, "CREATE DATABASE memories_selection"); err != nil {
		t.Fatal(err)
	}
	if err := closeBootstrap(); err != nil {
		t.Fatal(err)
	}
	database, cleanup, err := embeddeddolt.OpenSQL(ctx, dir, "memories_selection", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cleanup() })
	database.SetMaxOpenConns(1)
	if _, err := database.ExecContext(ctx, "CREATE TABLE config (`key` VARCHAR(255) PRIMARY KEY, value LONGTEXT)"); err != nil {
		t.Fatal(err)
	}
	seed, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = seed.Rollback() })
	values := map[string]string{
		"issue_prefix":                         "selection",
		"kv.other":                             "fixed neighboring KV",
		"kv.memory.direction":                  "fixed project direction",
		"kv.memory.@knowledge/record/solution": "FIXED preserved solution",
		"kv.memory.@knowledge/record/old":      "fixed old solution",
		"kv.memory.@knowledge/Record/case":     "fixed distinct case",
		"kv.memory.%_\\literal/é":              "fixed literal unicode",
		"kv.memory.%Xliteral/e":                "fixed wildcard neighbor",
		"kv.memory.é/empty":                    "",
	}
	for i := 0; i < 1024; i++ {
		values[fmt.Sprintf("kv.memory.@knowledge/code/file%04d", i)] = strings.Repeat("x", 8192)
	}
	for key, value := range values {
		if _, err := seed.ExecContext(ctx, "INSERT INTO config (`key`, value) VALUES (?, ?)", key, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := seed.Commit(); err != nil {
		t.Fatal(err)
	}
	plane := storagememoryops.MemoriesFromConfig(values)
	for _, tc := range []struct {
		name     string
		req      memoryops.ListRequest
		maxBytes int
	}{
		{"human", memoryops.ListRequest{ExcludeKeyPrefix: "@"}, 512},
		{"records", memoryops.ListRequest{KeyPrefix: "@knowledge/record/", ExcludeKeyPrefix: "@knowledge/record/old", Search: "fixed"}, 128},
		{"literal", memoryops.ListRequest{KeyPrefix: "%_\\literal/é", Search: "FIXED"}, 128},
		{"unicode", memoryops.ListRequest{KeyPrefix: "é/"}, 128},
		{"miss", memoryops.ListRequest{KeyPrefix: "absent/"}, 0},
		{"empty selectors", memoryops.ListRequest{}, 9 * 1024 * 1024},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := database.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			observed := &memorySelectionObservation{ConfigUseCase: domain.NewConfigUseCase(db.NewConfigSQLRepository(tx))}
			uw := &mockUnitOfWork{configUseCase: observed}
			provider := &mockUnitOfWorkProvider{uows: []*mockUnitOfWork{uw}}
			role, err := NewMemories(provider)
			if err != nil {
				t.Fatal(err)
			}
			got, err := role.List(ctx, tc.req)
			if err != nil {
				t.Fatal(err)
			}
			if want := memoryapi.SelectMemories(plane, tc.req); !reflect.DeepEqual(got.Memories, want) {
				t.Fatalf("selected memories differ: got %d entries, want %d", len(got.Memories), len(want))
			}
			if observed.bytes > tc.maxBytes {
				t.Errorf("transferred %d key/value bytes; selected namespace budget %d (full fixture %d)", observed.bytes, tc.maxBytes, memorySelectionBytes(values))
			}
			if !uw.closed || uw.commitCount != 0 || provider.newUOWCalls != 1 {
				t.Fatalf("read must close one UOW without commit: %+v", uw)
			}
			t.Logf("repository returned %d key/value bytes from %d-byte config fixture", observed.bytes, memorySelectionBytes(values))
		})
	}
}

type memorySelectionObservation struct {
	domain.ConfigUseCase
	bytes int
}

func (o *memorySelectionObservation) GetAllConfig(ctx context.Context) (map[string]string, error) {
	values, err := o.ConfigUseCase.GetAllConfig(ctx)
	o.bytes += memorySelectionBytes(values)
	return values, err
}

func (o *memorySelectionObservation) GetConfigByPrefix(ctx context.Context, prefix, excluded string) (map[string]string, error) {
	values, err := o.ConfigUseCase.GetConfigByPrefix(ctx, prefix, excluded)
	o.bytes += memorySelectionBytes(values)
	return values, err
}

func memorySelectionBytes(values map[string]string) int {
	count := 0
	for key, value := range values {
		count += len(key) + len(value)
	}
	return count
}
