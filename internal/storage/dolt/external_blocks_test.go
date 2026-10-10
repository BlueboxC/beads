package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// Exercise the real SQL-server adapter, including its selection/claim transaction.
func TestExternalBlocksSQLServerSelection(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := t.Context()
	for _, id := range []string{"test-ext-blocked", "test-ext-free"} {
		if err := store.CreateIssue(ctx, &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, "tester"); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.AddDependency(ctx, &types.Dependency{IssueID: "test-ext-blocked", DependsOnID: "external:peer:cap", Type: types.DepBlocks}, "tester"); err != nil {
		t.Fatal(err)
	}
	rows, err := store.GetReadyWork(ctx, types.WorkFilter{Limit: 1, SortPolicy: types.SortPolicyOldest})
	if err != nil || len(rows) != 1 || rows[0].ID != "test-ext-free" {
		t.Fatalf("server ready = %v, %v", rows, err)
	}
	count, err := store.CountReadyWork(ctx, types.WorkFilter{})
	if err != nil || count != 1 {
		t.Fatalf("server count = %d, %v", count, err)
	}
	claimer, err := store.ReadyClaimer()
	if err != nil {
		t.Fatal(err)
	}
	claim, err := claimer.ClaimNext(ctx, issueops.ClaimNextRequest{Actor: "tester"})
	if err != nil || claim.Claimed == nil || claim.Claimed.ID != "test-ext-free" {
		t.Fatalf("server claim = %v, %v", claim, err)
	}
	claim, err = claimer.ClaimNext(ctx, issueops.ClaimNextRequest{Actor: "tester"})
	if err != nil || claim.Claimed != nil {
		t.Fatalf("server blocked claim = %v, %v", claim, err)
	}
	released := issueops.WithExternalResolver(ctx, func(context.Context, []string) (map[string]bool, error) {
		return map[string]bool{"external:peer:cap": true}, nil
	})
	claim, err = claimer.ClaimNext(released, issueops.ClaimNextRequest{Actor: "tester"})
	if err != nil || claim.Claimed == nil || claim.Claimed.ID != "test-ext-blocked" {
		t.Fatalf("server released claim = %v, %v", claim, err)
	}
}
