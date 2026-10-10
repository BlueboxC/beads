//go:build cgo

package graphstore

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	graph "github.com/steveyegge/beads/graphops"
)

// Pair acquisition must keep using the destination's deletion-aware decoder.
// Normal APIs author every accepted state; the final SQL change is an explicit
// corruption control, not a shortcut for constructing retained demonstration data.
func TestReadVersionPairDeletedMemory(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, options := issueExperimentOptions(t, backend)
			s, err := OpenExisting(ctx, options)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := s.Close(); err != nil {
					t.Error(err)
				}
			})
			// Empty content remains a present retained Memory. This regression
			// follows sjarmak's presence scenario in gastownhall/beads#5964;
			// that contributor's ordinary KV-memory implementation stays separate.
			first, err := s.Create(ctx, CreateRequest{Path: "beads/plan", Title: "Plan", Body: "", Actor: "author"})
			if err != nil {
				t.Fatal(err)
			}
			survivor, err := s.CreateIssue(ctx, "beads/work", plainIssue("Surviving work"))
			if err != nil {
				t.Fatal(err)
			}
			linked, err := s.AddInformationalLink(ctx, LinkCreateRequest{Path: "links/context", SourcePath: "beads/plan", TargetPath: "beads/work", ExpectedSourceRevision: first.Revision, Properties: map[string]any{"note": "retained context"}})
			if err != nil {
				t.Fatal(err)
			}
			owned := linked.Source.(Record)
			edited, err := s.UpdateMemory(ctx, MemoryUpdateRequest{Path: "beads/plan", ExpectedRevision: owned.Revision, Properties: Properties{Title: "Plan", Body: "final body — 雪"}, Actor: "editor"})
			if err != nil {
				t.Fatal(err)
			}
			removed, err := s.Unlink(ctx, LinkDeleteRequest{Path: "links/context", ExpectedRevision: linked.Link.Revision, ExpectedSourceRevision: edited.Memory.Revision})
			if err != nil {
				t.Fatal(err)
			}
			final := removed.Source.(Record)
			deleted, err := s.DeleteMemory(ctx, MemoryDeleteRequest{Path: "beads/plan", ExpectedRevision: final.Revision, Actor: "deleter"})
			if err != nil || !deleted.Deleted || !reflect.DeepEqual(deleted.Memory, final) {
				t.Fatalf("delete final live state: %+v %v", deleted, err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = OpenExisting(ctx, options)
			if err != nil {
				t.Fatal(err)
			}
			before := workflowState(t, ctx, s)
			if _, err := s.Read(ctx, "beads/plan"); !errors.Is(err, ErrGone) {
				t.Fatalf("deleted current Memory: %v", err)
			}
			if got, err := s.Read(ctx, "beads/work"); err != nil || !reflect.DeepEqual(got, survivor) {
				t.Fatalf("surviving Issue changed: %+v %v", got, err)
			}
			if got, err := s.CurrentSnapshot(ctx); err != nil || len(got.Records) != 1 || !reflect.DeepEqual(got.Records[0], survivor) {
				t.Fatalf("deleted Memory/Link leaked into current snapshot: %+v %v", got, err)
			}
			for _, from := range []Record{first, owned, final} {
				for _, to := range []Record{first, owned, final} {
					got, err := s.ReadVersionPair(ctx, "beads/plan", from.Version, to.Version)
					if err != nil || !reflect.DeepEqual(got, VersionPair{From: from, To: to}) {
						t.Fatalf("deleted Memory %s -> %s: %+v %v", from.Version, to.Version, got, err)
					}
				}
			}
			for _, tokens := range [][2]string{{first.Version, "unknown"}, {"unknown", final.Version}, {"unknown", "unknown"}, {first.Version, linked.Link.Version}} {
				got, err := s.ReadVersionPair(ctx, "beads/plan", tokens[0], tokens[1])
				if !errors.Is(err, ErrVersionUnknown) || !reflect.DeepEqual(got, VersionPair{}) {
					t.Fatalf("deleted Memory invalid operand leaked a pair: %+v %v", got, err)
				}
				comparison, err := s.CompareVersions(ctx, "beads/plan", tokens[0], tokens[1])
				if !errors.Is(err, ErrVersionUnknown) || !reflect.DeepEqual(comparison, VersionComparison{}) {
					t.Fatalf("deleted Memory invalid operand leaked a comparison: %+v %v", comparison, err)
				}
			}
			for _, reverse := range []bool{false, true} {
				from, to := owned, final
				if reverse {
					from, to = to, from
				}
				comparison, err := s.CompareVersions(ctx, "beads/plan", from.Version, to.Version)
				if err != nil || comparison.Resource.ID != first.ID || comparison.From.Version != from.Version || comparison.To.Version != to.Version || len(comparison.Changes) != 2 {
					t.Fatalf("deleted Memory comparison: %+v %v", comparison, err)
				}
				for _, change := range comparison.Changes {
					switch change.Area {
					case "properties":
						var oldBody, newBody string
						if err := json.Unmarshal(change.From.Value, &oldBody); err != nil {
							t.Fatal(err)
						}
						if err := json.Unmarshal(change.To.Value, &newBody); err != nil {
							t.Fatal(err)
						}
						if change.Member != "body" || !change.From.Present || !change.To.Present || oldBody != from.Properties.Body || newBody != to.Properties.Body {
							t.Fatalf("body presence/direction changed: %+v", change)
						}
					case "owned":
						present := change.From
						if reverse {
							present = change.To
						}
						var retained LinkRecord
						if err := json.Unmarshal(present.Value, &retained); err != nil {
							t.Fatal(err)
						}
						if change.ID != linked.Link.ID || change.From.Present != !reverse || change.To.Present != reverse || !reflect.DeepEqual(retained, linked.Link) {
							t.Fatalf("retained owned Link changed: %+v", change)
						}
					default:
						t.Fatalf("unexpected comparison domain: %+v", change)
					}
				}
			}
			comparison, err := s.CompareVersions(ctx, "beads/plan", final.Version, final.Version)
			if err != nil || len(comparison.Changes) != 0 || comparison.From.Version != final.Version || comparison.To.Version != final.Version {
				t.Fatalf("deleted Memory equal-token comparison: %+v %v", comparison, err)
			}
			if !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
				t.Fatal("deleted Memory reads/comparisons changed current or retained state")
			}
			// A valid old operand does not bypass the deleted final-head check.
			if _, err := s.db.ExecContext(ctx, `UPDATE graph_preview_versions SET snapshot='{}' WHERE path=? AND version=?`, "beads/plan", final.Version); err != nil {
				t.Fatal(err)
			}
			before = workflowState(t, ctx, s)
			if got, err := s.ReadVersionPair(ctx, "beads/plan", first.Version, final.Version); !errors.Is(err, ErrInvalidStore) || !reflect.DeepEqual(got, VersionPair{}) {
				t.Fatalf("corrupt deleted head returned pair: %+v %v", got, err)
			}
			if got, err := s.CompareVersions(ctx, "beads/plan", first.Version, final.Version); !errors.Is(err, ErrInvalidStore) || !reflect.DeepEqual(got, VersionComparison{}) {
				t.Fatalf("corrupt deleted head returned comparison: %+v %v", got, err)
			}
			if !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
				t.Fatal("failed deleted Memory reads repaired retained state")
			}
		})
	}
}

func TestReadVersionPairInputAdmission(t *testing.T) {
	// A store without a connector demonstrates that BOTH tokens are validated
	// before acquisition, even when the first token would otherwise be admissible.
	s := &Store{}
	for _, bad := range []string{"", string([]byte{255}), strings.Repeat("t", PreviewVersionTokenLimit+1)} {
		for _, tokens := range [][2]string{{bad, "valid"}, {"valid", bad}} {
			got, err := s.ReadVersionPair(nil, "beads/plan", tokens[0], tokens[1])
			if !errors.Is(err, graph.ErrValidation) || !reflect.DeepEqual(got, VersionPair{}) {
				t.Fatalf("invalid token: %+v %v", got, err)
			}
		}
	}
	if got, err := s.ReadVersionPair(nil, "not-a-resource", "valid", "valid"); err == nil || !reflect.DeepEqual(got, VersionPair{}) {
		t.Fatalf("invalid path: %+v %v", got, err)
	}
}

func TestReadVersionPairLifecycle(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, o := issueExperimentOptions(t, backend)
			s, err := OpenExisting(ctx, o)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := s.Close(); err != nil {
					t.Error(err)
				}
			}()
			memory, err := s.Create(ctx, CreateRequest{Path: "beads/plan", Title: "Plan", Body: "Before — 雪\n", Actor: "author"})
			if err != nil {
				t.Fatal(err)
			}
			work, err := s.CreateIssue(ctx, "beads/work", plainIssue("Work"))
			if err != nil {
				t.Fatal(err)
			}
			prereq, err := s.CreateIssue(ctx, "beads/prereq", plainIssue("Prerequisite"))
			if err != nil {
				t.Fatal(err)
			}
			dependency, err := s.AddDependency(ctx, DependencyRequest{Path: "links/block", SourcePath: "beads/work", TargetPath: "beads/prereq", Actor: "author"})
			if err != nil {
				t.Fatal(err)
			}
			closed, err := s.CloseIssue(ctx, "beads/prereq", "Done", "closer")
			if err != nil {
				t.Fatal(err)
			}
			link, err := s.AddInformationalLink(ctx, LinkCreateRequest{Path: "links/context", SourcePath: "beads/plan", TargetPath: "beads/work", ExpectedSourceRevision: memory.Revision, Properties: map[string]any{"note": "before"}})
			if err != nil {
				t.Fatal(err)
			}
			edited, err := s.UpdateMemory(ctx, MemoryUpdateRequest{Path: "beads/plan", ExpectedRevision: link.Source.(Record).Revision, Properties: Properties{Title: "Plan", Body: "After — 雪\n"}, Actor: "editor"})
			if err != nil {
				t.Fatal(err)
			}
			changedLink, err := s.UpdateLink(ctx, LinkUpdateRequest{Path: "links/context", ExpectedRevision: link.Link.Revision, ExpectedSourceRevision: edited.Memory.Revision, Properties: map[string]any{"note": "after"}})
			if err != nil {
				t.Fatal(err)
			}
			removed, err := s.Unlink(ctx, LinkDeleteRequest{Path: "links/context", ExpectedRevision: changedLink.Link.Revision, ExpectedSourceRevision: changedLink.Source.(Record).Revision})
			if err != nil {
				t.Fatal(err)
			}
			// Current-only Issue row locks and content hashes are not retained JSON.
			historicalIssue := func(r IssueRecord) IssueRecord {
				p := *r.Properties
				p.ContentHash, p.RowVersion = "", 0
				r.Properties = &p
				return r
			}
			cases := []struct {
				path, from, to   string
				wantFrom, wantTo any
			}{
				{"beads/plan", memory.Version, edited.Memory.Version, memory, edited.Memory},
				{"beads/plan", edited.Memory.Version, changedLink.Source.(Record).Version, edited.Memory, changedLink.Source},
				{"beads/plan", changedLink.Source.(Record).Version, removed.Source.(Record).Version, changedLink.Source, removed.Source},
				{"beads/work", work.Version, dependency.Source.Version, historicalIssue(work), historicalIssue(dependency.Source)},
				{"beads/prereq", prereq.Version, closed.Issue.Version, historicalIssue(prereq), historicalIssue(closed.Issue)},
				{"links/context", link.Link.Version, changedLink.Link.Version, link.Link, changedLink.Link},
				{"links/block", dependency.Link.Version, dependency.Link.Version, dependency.Link, dependency.Link},
			}
			before := workflowState(t, ctx, s)
			for _, pass := range []string{"initial", "reopened"} {
				if pass == "reopened" {
					if err := s.Close(); err != nil {
						t.Fatal(err)
					}
					s, err = OpenExisting(ctx, o)
					if err != nil {
						t.Fatal(err)
					}
				}
				for _, tc := range cases {
					got, err := s.ReadVersionPair(ctx, tc.path, tc.from, tc.to)
					want := VersionPair{From: tc.wantFrom, To: tc.wantTo}
					if err != nil || !reflect.DeepEqual(got, want) {
						t.Fatalf("%s %s pair: got=%+v want=%+v err=%v", pass, tc.path, got, want, err)
					}
					reverse, err := s.ReadVersionPair(ctx, tc.path, tc.to, tc.from)
					if err != nil || !reflect.DeepEqual(reverse, VersionPair{From: tc.wantTo, To: tc.wantFrom}) {
						t.Fatalf("%s %s reverse: %+v %v", pass, tc.path, reverse, err)
					}
					same, err := s.ReadVersionPair(ctx, tc.path, tc.from, tc.from)
					if err != nil || !reflect.DeepEqual(same, VersionPair{From: tc.wantFrom, To: tc.wantFrom}) {
						t.Fatalf("%s %s same: %+v %v", pass, tc.path, same, err)
					}
					for _, tokens := range [][2]string{{tc.from, "unknown opaque token"}, {"unknown opaque token", tc.to}, {"unknown opaque token", "unknown opaque token"}} {
						got, err := s.ReadVersionPair(ctx, tc.path, tokens[0], tokens[1])
						if !errors.Is(err, ErrVersionUnknown) || !reflect.DeepEqual(got, VersionPair{}) {
							t.Fatalf("%s partial unknown pair: %+v %v", tc.path, got, err)
						}
					}
				}
			}
			for _, tc := range []struct {
				path, from, to string
				want           error
			}{
				{"links/context", link.Link.Version, removed.Link.Version, ErrGone},
				{"beads/plan", memory.Version, link.Link.Version, ErrVersionUnknown},
				{"beads/missing", memory.Version, memory.Version, ErrNotFound},
			} {
				got, err := s.ReadVersionPair(ctx, tc.path, tc.from, tc.to)
				if !errors.Is(err, tc.want) || !reflect.DeepEqual(got, VersionPair{}) {
					t.Fatalf("%s refusal: %+v %v", tc.path, got, err)
				}
			}
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			if got, err := s.ReadVersionPair(canceled, "beads/plan", memory.Version, edited.Memory.Version); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, VersionPair{}) {
				t.Fatalf("canceled pair: %+v %v", got, err)
			}
			s.options.Binding.AuthorityID = "ffffffffffffffffffffffffffffffff"
			got, err := s.ReadVersionPair(ctx, "beads/plan", memory.Version, edited.Memory.Version)
			s.options = o
			if !errors.Is(err, ErrInvalidStore) || !reflect.DeepEqual(got, VersionPair{}) {
				t.Fatalf("authority refusal: %+v %v", got, err)
			}
			if !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
				t.Fatal("pair reads/refusals changed writer or retained state")
			}
			// Current content cannot contaminate either historical operand.
			if _, err := s.db.ExecContext(ctx, `UPDATE graph_preview_payloads SET properties='{}' WHERE path='beads/plan'`); err != nil {
				t.Fatal(err)
			}
			before = workflowState(t, ctx, s)
			got, err = s.ReadVersionPair(ctx, "beads/plan", memory.Version, edited.Memory.Version)
			if err != nil || !reflect.DeepEqual(got, VersionPair{From: memory, To: edited.Memory}) {
				t.Fatalf("pair hydrated current payload: %+v %v", got, err)
			}
			if !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
				t.Fatal("pair read repaired current corruption")
			}
		})
	}
}

func TestReadVersionPairCorruptSecond(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, o := issueExperimentOptions(t, backend)
			s, err := OpenExisting(ctx, o)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := s.Close(); err != nil {
					t.Error(err)
				}
			}()
			memory, err := s.Create(ctx, CreateRequest{Path: "beads/plan", Body: "before"})
			if err != nil {
				t.Fatal(err)
			}
			issue, err := s.CreateIssue(ctx, "beads/work", plainIssue("Work"))
			if err != nil {
				t.Fatal(err)
			}
			closed, err := s.CloseIssue(ctx, "beads/work", "Done", "author")
			if err != nil {
				t.Fatal(err)
			}
			link, err := s.AddInformationalLink(ctx, LinkCreateRequest{Path: "links/context", SourcePath: "beads/plan", TargetPath: "beads/work", ExpectedSourceRevision: memory.Version})
			if err != nil {
				t.Fatal(err)
			}
			changed, err := s.UpdateLink(ctx, LinkUpdateRequest{Path: "links/context", ExpectedRevision: link.Link.Revision, ExpectedSourceRevision: link.Source.(Record).Revision, Properties: map[string]any{"note": "changed"}})
			if err != nil {
				t.Fatal(err)
			}
			// These real-engine fault controls are deliberately not demonstration data.
			for _, tc := range []struct {
				path, from, to, query string
				args                  []any
			}{
				{"beads/plan", memory.Version, changed.Source.(Record).Version, `UPDATE graph_preview_versions SET snapshot='{}' WHERE path=? AND version=?`, []any{"beads/plan", changed.Source.(Record).Version}},
				{"links/context", link.Link.Version, changed.Link.Version, `UPDATE graph_preview_versions SET snapshot='{}' WHERE path=? AND version=?`, []any{"links/context", changed.Link.Version}},
				{"beads/work", issue.Version, closed.Issue.Version, `UPDATE issue_versions SET durable_state='{}' WHERE issue_id=(SELECT backing_key FROM graph_preview_catalog WHERE path=?) AND revision=(SELECT issue_revision FROM graph_preview_issue_versions WHERE path=? AND version=?)`, []any{"beads/work", "beads/work", closed.Issue.Version}},
			} {
				if _, err := s.db.ExecContext(ctx, tc.query, tc.args...); err != nil {
					t.Fatal(err)
				}
				before := workflowState(t, ctx, s)
				got, err := s.ReadVersionPair(ctx, tc.path, tc.from, tc.to)
				if !errors.Is(err, ErrInvalidStore) || !reflect.DeepEqual(got, VersionPair{}) {
					t.Fatalf("%s corrupt second returned partial: %+v %v", tc.path, got, err)
				}
				if !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
					t.Fatalf("%s read changed corruption", tc.path)
				}
			}
			if _, err := s.db.ExecContext(ctx, `UPDATE graph_preview_versions SET snapshot=REPEAT('x',?) WHERE path='beads/plan' AND version=?`, PreviewCurrentReadByteLimit+1, changed.Source.(Record).Version); err != nil {
				t.Fatal(err)
			}
			got, err := s.ReadVersionPair(ctx, "beads/plan", memory.Version, changed.Source.(Record).Version)
			if !errors.Is(err, ErrLimitExceeded) || !reflect.DeepEqual(got, VersionPair{}) {
				t.Fatalf("oversized second returned partial: %+v %v", got, err)
			}
		})
	}
}
