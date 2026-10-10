//go:build cgo

package embeddeddolt_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

func TestExternalBlocksWithholdReadyWork(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "ext")
	ctx := t.Context()
	for _, id := range []string{"ext-parent", "ext-child", "ext-free"} {
		priority := 2
		if id == "ext-parent" {
			priority = 1
		}
		if err := te.store.CreateIssue(ctx, &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: priority, IssueType: types.TypeTask}, "tester"); err != nil {
			t.Fatal(err)
		}
	}
	for _, dep := range []*types.Dependency{
		{IssueID: "ext-parent", DependsOnID: "external:upstream:cap", Type: types.DepBlocks},
		{IssueID: "ext-child", DependsOnID: "ext-parent", Type: types.DepParentChild},
	} {
		if err := te.store.AddDependency(ctx, dep, "tester"); err != nil {
			t.Fatal(err)
		}
	}
	ready, err := te.store.GetReadyWork(ctx, types.WorkFilter{Limit: 1, SortPolicy: types.SortPolicyOldest})
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 1 || ready[0].ID != "ext-free" {
		t.Fatalf("ready = %v, want only ext-free", ready)
	}
	blocked, err := te.store.GetBlockedIssues(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked) != 2 {
		t.Fatalf("blocked = %v, want parent and child", blocked)
	}
	count, err := te.store.CountReadyWork(ctx, types.WorkFilter{})
	if err != nil || count != 1 {
		t.Fatalf("count = %d, %v, want 1", count, err)
	}
	domainIDs, err := readyWorkIDs(t, te)
	if err != nil || !slices.Equal(domainIDs, []string{"ext-free"}) {
		t.Fatalf("domain ready = %v, %v", domainIDs, err)
	}
	counted, err := te.store.GetReadyWorkWithCounts(ctx, types.WorkFilter{Limit: 1})
	if err != nil || len(counted) != 1 || counted[0].ID != "ext-free" {
		t.Fatalf("counted ready = %v, %v", counted, err)
	}
	claimer, err := te.store.ReadyClaimer()
	if err != nil {
		t.Fatal(err)
	}
	claim, err := claimer.ClaimNext(ctx, issueops.ClaimNextRequest{Actor: "tester"})
	if err != nil || claim.Claimed == nil || claim.Claimed.ID != "ext-free" {
		t.Fatalf("claim = %v, %v", claim, err)
	}
	claim, err = claimer.ClaimNext(ctx, issueops.ClaimNextRequest{Actor: "tester"})
	if err != nil || claim.Claimed != nil {
		t.Fatalf("blocked claim = %v, %v", claim, err)
	}
	lookupCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	lookupCtx = issueops.WithExternalResolver(lookupCtx, func(context.Context, []string) (map[string]bool, error) {
		cancel()
		return map[string]bool{"external:upstream:cap": true}, nil
	})
	if _, err := te.store.GetReadyWork(lookupCtx, types.WorkFilter{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("accepted provider result after cancellation: %v", err)
	}
	failure := errors.New("provider read failed")
	failedCtx := issueops.WithExternalResolver(ctx, func(context.Context, []string) (map[string]bool, error) { return nil, failure })
	if _, err := te.store.GetReadyWork(failedCtx, types.WorkFilter{}); !errors.Is(err, failure) {
		t.Fatalf("provider error was hidden: %v", err)
	}
	releasedCtx := issueops.WithExternalResolver(ctx, func(_ context.Context, refs []string) (map[string]bool, error) {
		if !slices.Equal(refs, []string{"external:upstream:cap"}) {
			t.Fatalf("references = %v", refs)
		}
		return map[string]bool{"external:upstream:cap": true}, nil
	})
	released, err := te.store.GetReadyWork(releasedCtx, types.WorkFilter{})
	if err != nil || len(released) != 3 {
		t.Fatalf("released = %v, %v", released, err)
	}
	releasedBlocked, err := te.store.GetBlockedIssues(releasedCtx, types.WorkFilter{})
	if err != nil || len(releasedBlocked) != 0 {
		t.Fatalf("released blocked = %v, %v", releasedBlocked, err)
	}
	releasedCount, err := te.store.CountReadyWork(releasedCtx, types.WorkFilter{})
	if err != nil || releasedCount != 3 {
		t.Fatalf("released count = %d, %v", releasedCount, err)
	}
	claim, err = claimer.ClaimNext(releasedCtx, issueops.ClaimNextRequest{Actor: "tester"})
	if err != nil || claim.Claimed == nil || claim.Claimed.ID != "ext-parent" {
		t.Fatalf("released claim = %v, %v", claim, err)
	}
}

func TestExternalBlocksCrossIssueWispDescendants(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "exttree")
	ctx := t.Context()
	for _, row := range []*types.Issue{
		{ID: "exttree-parent", Title: "parent", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "exttree-wisp", Title: "wisp", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true},
		{ID: "exttree-grandchild", Title: "grandchild", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "exttree-free", Title: "free", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	} {
		if err := te.store.CreateIssue(ctx, row, "tester"); err != nil {
			t.Fatal(err)
		}
	}
	for _, dep := range []*types.Dependency{
		{IssueID: "exttree-parent", DependsOnID: "external:upstream:cap", Type: types.DepBlocks},
		{IssueID: "exttree-wisp", DependsOnID: "exttree-parent", Type: types.DepParentChild},
		{IssueID: "exttree-grandchild", DependsOnID: "exttree-wisp", Type: types.DepParentChild},
	} {
		if err := te.store.AddDependency(ctx, dep, "tester"); err != nil {
			t.Fatal(err)
		}
	}
	filter := types.WorkFilter{IncludeEphemeral: true, Limit: 1, SortPolicy: types.SortPolicyOldest}
	rows, err := te.store.GetReadyWork(ctx, filter)
	if err != nil || len(rows) != 1 || rows[0].ID != "exttree-free" {
		t.Fatalf("cross-plane ready = %v, %v", rows, err)
	}
	blocked, err := te.store.GetBlockedIssues(ctx, types.WorkFilter{})
	if err != nil || len(blocked) != 3 {
		t.Fatalf("cross-plane blocked = %v, %v", blocked, err)
	}
	released := issueops.WithExternalResolver(ctx, func(context.Context, []string) (map[string]bool, error) {
		return map[string]bool{"external:upstream:cap": true}, nil
	})
	filter.Limit = 0
	rows, err = te.store.GetReadyWork(released, filter)
	if err != nil || len(rows) != 4 {
		t.Fatalf("cross-plane released = %v, %v", rows, err)
	}
}
