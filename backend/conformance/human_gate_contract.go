package conformance

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
	"github.com/steveyegge/beads/journalops"
)

type HumanGateSource interface {
	IssueLifecycle() (publicops.Lifecycle, error)
	DependencyEditor() (publicops.DependencyEditor, error)
	Deleter() (publicops.Deleter, error)
	BatchCloser() (publicops.BatchCloser, error)
	BatchApplier() (publicops.BatchApplier, error)
}

type HumanGateFixture struct {
	Source        HumanGateSource
	Prefix        string
	CreateIssue   func(context.Context, *types.Issue, string) error
	CreateWisp    func(context.Context, *types.Issue, string) error
	AddDependency func(context.Context, *types.Dependency, string) error
	SetConfig     func(context.Context, string, string) error
	QueryScalar   func(context.Context, string, []any, ...any) error
}

// CheckHumanGatePolicy qualifies the fork policy across existing mutation roles.
// It needs SQL seed/cursor access beyond the portable role fixtures.
func CheckHumanGatePolicy(t *testing.T, ctx context.Context, f HumanGateFixture) {
	t.Helper()
	lifecycle, err := f.Source.IssueLifecycle()
	if err != nil {
		t.Fatal(err)
	}
	editor, err := f.Source.DependencyEditor()
	if err != nil {
		t.Fatal(err)
	}
	deleter, err := f.Source.Deleter()
	if err != nil {
		t.Fatal(err)
	}
	closer, err := f.Source.BatchCloser()
	if err != nil {
		t.Fatal(err)
	}
	applier, err := f.Source.BatchApplier()
	if err != nil {
		t.Fatal(err)
	}
	set := func(value string) {
		t.Helper()
		if err := f.SetConfig(ctx, "gates.human.resolvers", value); err != nil {
			t.Fatal(err)
		}
	}
	seed := func(t *testing.T, suffix string, wisp bool) (string, string, string) {
		t.Helper()
		gate, task, ordinary := f.Prefix+"-g-"+suffix, f.Prefix+"-t-"+suffix, f.Prefix+"-o-"+suffix
		create := f.CreateIssue
		if wisp {
			create = f.CreateWisp
		}
		for _, id := range []string{gate, task, ordinary} {
			row := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2, Ephemeral: wisp}
			if id == gate {
				row.IssueType = types.TypeGate
				row.AwaitType = "human"
			}
			if err := create(ctx, row, "seed"); err != nil {
				t.Fatal(err)
			}
		}
		if err := f.AddDependency(ctx, &types.Dependency{IssueID: task, DependsOnID: gate, Type: types.DepBlocks, Metadata: `{"original":true}`}, "seed"); err != nil {
			t.Fatal(err)
		}
		return gate, task, ordinary
	}
	unchanged := func(t *testing.T, gate, task, ordinary string) {
		t.Helper()
		for _, id := range []string{gate, task, ordinary} {
			var status, kind string
			q := "SELECT status,issue_type FROM issues WHERE id = ? UNION ALL SELECT status,issue_type FROM wisps WHERE id = ?"
			if err := f.QueryScalar(ctx, q, []any{id, id}, &status, &kind); err != nil {
				t.Fatal(err)
			}
			if status != "open" || (id == gate && kind != "gate") {
				t.Fatalf("mutated %s: %s/%s", id, status, kind)
			}
		}
		var edges int
		q := "SELECT (SELECT COUNT(*) FROM dependencies WHERE issue_id=? AND depends_on_issue_id=?) + (SELECT COUNT(*) FROM wisp_dependencies WHERE issue_id=? AND depends_on_wisp_id=?)"
		if err := f.QueryScalar(ctx, q, []any{task, gate, task, gate}, &edges); err != nil {
			t.Fatal(err)
		}
		if edges != 1 {
			t.Fatalf("gate edge count = %d", edges)
		}
	}
	tests := []struct {
		name string
		run  func(context.Context, string, string, string) error
	}{
		{"close", func(c context.Context, g, _, _ string) error {
			_, e := lifecycle.Close(c, publicops.CloseRequest{Actor: "agent", IssueID: g})
			return e
		}},
		{"force-close", func(c context.Context, g, _, _ string) error {
			_, e := lifecycle.Close(c, publicops.CloseRequest{Actor: "agent", IssueID: g, Force: true})
			return e
		}},
		{"force-close-dependent", func(c context.Context, _, s, _ string) error {
			_, e := lifecycle.Close(c, publicops.CloseRequest{Actor: "agent", IssueID: s, Force: true})
			return e
		}},
		{"update-closed", func(c context.Context, g, _, _ string) error {
			_, e := lifecycle.Update(c, publicops.UpdateRequest{Actor: "agent", IssueID: g, ForceClosePolicy: true, Patch: publicops.IssuePatch{Status: publicops.Field[publicops.Status]{Set: true, Value: types.StatusClosed}}})
			return e
		}},
		{"update-type", func(c context.Context, g, _, _ string) error {
			_, e := lifecycle.Update(c, publicops.UpdateRequest{Actor: "agent", IssueID: g, Patch: publicops.IssuePatch{IssueType: publicops.Field[publicops.IssueType]{Set: true, Value: types.TypeTask}}})
			return e
		}},
		{"update-defer", func(c context.Context, g, _, _ string) error {
			when := time.Now().Add(time.Hour)
			_, e := lifecycle.Update(c, publicops.UpdateRequest{Actor: "agent", IssueID: g, Patch: publicops.IssuePatch{DeferUntil: publicops.Field[*time.Time]{Set: true, Value: &when}}})
			return e
		}},
		{"update-dependent-closed", func(c context.Context, _, task, _ string) error {
			_, e := lifecycle.Update(c, publicops.UpdateRequest{Actor: "agent", IssueID: task, ForceClosePolicy: true, Patch: publicops.IssuePatch{Status: publicops.Field[publicops.Status]{Set: true, Value: types.StatusClosed}}})
			return e
		}},
		{"delete-dependent", func(c context.Context, _, task, _ string) error {
			_, e := deleter.Delete(c, publicops.DeleteRequest{Actor: "agent", IDs: []string{task}, Force: true})
			return e
		}},
		{"refresh-blocking-edge", func(c context.Context, g, task, _ string) error {
			_, e := editor.AddDependencies(c, publicops.AddDependenciesRequest{Actor: "agent", Edges: []publicops.DependencyEdge{{IssueID: task, DependsOnID: g, Type: types.DepBlocks}}})
			return e
		}},
		{"update-persistence", func(c context.Context, g, _, _ string) error {
			_, e := lifecycle.Update(c, publicops.UpdateRequest{Actor: "agent", IssueID: g, Patch: publicops.IssuePatch{Persistence: publicops.Field[publicops.PersistenceMode]{Set: true, Value: types.PersistenceModeNoHistory}}})
			return e
		}},
		{"actorless-delete", func(c context.Context, g, _, _ string) error {
			_, e := deleter.Delete(c, publicops.DeleteRequest{IDs: []string{g}, Force: true})
			return e
		}},
		{"delete-force", func(c context.Context, g, _, _ string) error {
			_, e := deleter.Delete(c, publicops.DeleteRequest{Actor: "agent", IDs: []string{g}, Force: true})
			return e
		}},
		{"delete-cascade", func(c context.Context, g, _, _ string) error {
			_, e := deleter.Delete(c, publicops.DeleteRequest{Actor: "agent", IDs: []string{g}, Cascade: true})
			return e
		}},
		{"unlink", func(c context.Context, g, s, _ string) error {
			_, e := editor.RemoveDependency(c, publicops.RemoveDependencyRequest{Actor: "agent", IssueID: s, DependsOnID: g})
			return e
		}},
		{"batch-rollback", func(c context.Context, g, _, o string) error {
			_, e := applier.ApplyBatch(c, publicops.ApplyBatchRequest{Actor: "agent", Items: []publicops.ApplyItem{{Kind: publicops.ItemClose, Close: &publicops.CloseItem{Target: publicops.Ref{ID: o}}}, {Kind: publicops.ItemClose, Close: &publicops.CloseItem{Target: publicops.Ref{ID: g}, Force: true}}}})
			return e
		}},
	}
	t.Run("unconfigured-compatible", func(t *testing.T) {
		g, _, _ := seed(t, "default", false)
		if _, err := lifecycle.Close(ctx, publicops.CloseRequest{Actor: "agent", IssueID: g}); err != nil {
			t.Fatal(err)
		}
	})
	set(`["owner"]`)
	for i, tc := range tests {
		for _, wisp := range []bool{false, true} {
			name := tc.name
			if wisp {
				name += "-wisp"
			}
			t.Run(name, func(t *testing.T) {
				suffix := name
				gate, task, ordinary := seed(t, suffix, wisp)
				var before, after int64
				if err := f.QueryScalar(ctx, "SELECT next_seq FROM bd_events_seq WHERE id=0", nil, &before); err != nil {
					t.Fatal(err)
				}
				err := tc.run(ctx, gate, task, ordinary)
				if err == nil || !strings.Contains(err.Error(), "human gate") {
					t.Fatalf("%d: expected human gate refusal, got %v", i, err)
				}
				if err := f.QueryScalar(ctx, "SELECT next_seq FROM bd_events_seq WHERE id=0", nil, &after); err != nil {
					t.Fatal(err)
				}
				if before != after {
					t.Fatalf("refusal advanced journal from %d to %d", before, after)
				}
				unchanged(t, gate, task, ordinary)
			})
		}
	}
	t.Run("fallback-owner-is-not-explicit", func(t *testing.T) {
		g, s, o := seed(t, "git", false)
		_, err := lifecycle.Close(journalops.WithActorSource(ctx, "owner", "git"), publicops.CloseRequest{Actor: "owner", IssueID: g, Force: true})
		if err == nil || !strings.Contains(err.Error(), "human gate") {
			t.Fatalf("fallback owner admitted: %v", err)
		}
		unchanged(t, g, s, o)
	})
	t.Run("listed-explicit-owner-resolves", func(t *testing.T) {
		g, s, _ := seed(t, "owner", false)
		_, err := lifecycle.Close(journalops.WithActorSource(ctx, "owner", "flag"), publicops.CloseRequest{Actor: "owner", IssueID: g})
		if err != nil {
			t.Fatal(err)
		}
		var blocked bool
		if err := f.QueryScalar(ctx, "SELECT is_blocked FROM issues WHERE id=?", []any{s}, &blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			t.Fatal("authorized close left task blocked")
		}
		if _, err := lifecycle.Close(ctx, publicops.CloseRequest{Actor: "agent", IssueID: s}); err != nil {
			t.Fatalf("resolved gate still vetoed ordinary work: %v", err)
		}

	})
	t.Run("best-effort-close-retains-refusal", func(t *testing.T) {
		g, s, o := seed(t, "best", false)
		result, err := closer.CloseBatch(ctx, publicops.CloseBatchRequest{Actor: "agent", Force: true, Items: []publicops.BatchCloseItem{{IssueID: o}, {IssueID: g}}})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Outcomes) != 2 || result.Outcomes[0].Err != nil || result.Outcomes[1].Err == nil {
			t.Fatalf("outcomes = %+v", result.Outcomes)
		}
		var status string
		if err := f.QueryScalar(ctx, "SELECT status FROM issues WHERE id=?", []any{g}, &status); err != nil {
			t.Fatal(err)
		}
		if status != "open" {
			t.Fatal("best-effort batch closed human gate")
		}
		var blocked bool
		if err := f.QueryScalar(ctx, "SELECT is_blocked FROM issues WHERE id=?", []any{s}, &blocked); err != nil {
			t.Fatal(err)
		}
		if !blocked {
			t.Fatal("best-effort batch unblocked human dependent")
		}
	})
	t.Run("deny-all-and-malformed", func(t *testing.T) {
		g, s, o := seed(t, "invalid", false)
		for _, value := range []string{`[]`, `null`, `[1]`, `[""]`, `{"owner":true}`, ``} {
			set(value)
			_, err := lifecycle.Close(ctx, publicops.CloseRequest{Actor: "owner", IssueID: g, Force: true})
			if err == nil {
				t.Fatalf("policy %q admitted", value)
			}
			unchanged(t, g, s, o)
		}
		set(`["owner"]`)
	})
}
