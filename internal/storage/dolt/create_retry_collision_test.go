package dolt

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// Two transactions deliberately choose the same generated ID. A rolled-back
// create must generate again, rather than importing over the winning row.
func TestCreateRetryPreservesConcurrentWriters(t *testing.T) {
	store, cleanup := setupLocalServerStore(t)
	defer cleanup()
	store.db.SetMaxOpenConns(4)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	for _, batch := range []bool{false, true} {
		for _, wisp := range []bool{false, true} {
			t.Run(fmt.Sprintf("batch=%v/wisp=%v", batch, wisp), func(t *testing.T) {
				for round := 0; round < 4; round++ {
					title := fmt.Sprintf("collision-%v-%v-%d", batch, wisp, round)
					created := time.Date(2026, 10, 6, 0, 0, round, 0, time.UTC)
					issues := make([]*types.Issue, 2)
					errs := make([]error, 2)
					start := make(chan struct{})
					var wg sync.WaitGroup
					for writer := 0; writer < 2; writer++ {
						issues[writer] = &types.Issue{Title: title, Description: "same hash inputs", Notes: fmt.Sprintf("writer-%d", writer), Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, CreatedAt: created, Ephemeral: wisp}
						wg.Add(1)
						go func(w int) {
							defer wg.Done()
							<-start
							if batch {
								errs[w] = store.CreateIssuesWithFullOptions(ctx, []*types.Issue{issues[w]}, "same-actor", storage.BatchCreateOptions{})
							} else {
								errs[w] = store.CreateIssue(ctx, issues[w], "same-actor")
							}
						}(writer)
					}
					close(start)
					wg.Wait()
					for writer, err := range errs {
						if err != nil {
							t.Fatalf("writer %d: %v", writer, err)
						}
					}
					if issues[0].ID == issues[1].ID {
						t.Fatalf("both writers returned success with ID %s; one task was overwritten", issues[0].ID)
					}
					for writer, issue := range issues {
						got, err := store.GetIssue(ctx, issue.ID)
						if err != nil {
							t.Fatal(err)
						}
						if got.Notes != issue.Notes {
							t.Fatalf("writer %d lost notes: %q", writer, got.Notes)
						}
						table := "events"
						if wisp {
							table = "wisp_events"
						}
						var count int
						if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE issue_id = ? AND event_type = 'created'", issue.ID).Scan(&count); err != nil {
							t.Fatal(err)
						}
						if count != 1 {
							t.Fatalf("created events for %s: %d, want 1", issue.ID, count)
						}
					}
				}
			})
		}
	}
	// Explicit IDs retain the import/upsert contract; only generated IDs reset.
	first := &types.Issue{ID: "test-explicit", Title: "original", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, first, "test"); err != nil {
		t.Fatal(err)
	}
	replacement := *first
	replacement.Title = "updated import"
	replacement.UpdatedAt = time.Now().Add(time.Second)
	if err := store.CreateIssues(ctx, []*types.Issue{&replacement}, "test"); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetIssue(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != replacement.Title {
		t.Fatalf("explicit ID upsert lost: %q", got.Title)
	}
}
