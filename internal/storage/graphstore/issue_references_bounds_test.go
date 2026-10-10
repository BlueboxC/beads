//go:build cgo

package graphstore

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// Reference replacement has an admitted inverse. A workspace-wide current-read
// refusal must not prevent shortening or clearing through the checked writer.
// Mixing append intent still carries append's existing readability postcondition.
func TestIssueReferencesExceedsCurrentReadBudget(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s, original, target, dependency := reopenFixture(t, backend)
			withReferences, err := s.UpdateIssue(ctx, UpdateIssueRequest{
				Path: "beads/work", Actor: "reference-author", ExpectedRevision: original.Revision,
				ExternalRef: issueEditString(strings.Repeat("e", 200)), SpecID: issueEditString(strings.Repeat("s", 900)),
			})
			if err != nil || !withReferences.Changed {
				t.Fatalf("author references: changed=%t err=%v", withReferences.Changed, err)
			}
			if got, err := s.Read(ctx, "beads/work"); err != nil || !reflect.DeepEqual(got, withReferences.Issue) {
				t.Fatalf("reference fixture is not initially readable: %v", err)
			}
			// Simulate canonical data admitted before write-budget enforcement.
			seedUnreadableMemory(t, ctx, s, "beads/large-context", strings.Repeat("m", 9<<20))
			assertOverBudget := func() {
				t.Helper()
				if got, err := s.Read(ctx, "beads/work"); !errors.Is(err, ErrLimitExceeded) || got != nil {
					t.Fatalf("current read must refuse over-budget workspace without partial record: err=%v", err)
				}
				if got, err := s.CurrentSnapshot(ctx); !errors.Is(err, ErrLimitExceeded) || !reflect.DeepEqual(got, Snapshot{}) {
					t.Fatalf("current inventory must refuse over-budget workspace without partial snapshot: err=%v", err)
				}
			}
			assertOverBudget()
			beforeEdits := reopenState(t, ctx, s)
			shortened, err := s.UpdateIssue(ctx, UpdateIssueRequest{
				Path: "beads/work", Actor: "shorten", ExpectedRevision: withReferences.Issue.Revision,
				ExternalRef: issueEditString("e"), SpecID: issueEditString("s"),
			})
			if err != nil || !shortened.Changed {
				t.Fatalf("guarded shortening must not require current-read acquisition: changed=%t err=%v", shortened.Changed, err)
			}
			assertIssueReferencesTransition(t, withReferences.Issue, shortened.Issue, issueEditString("e"), "s", original.Properties.Title, "shorten")
			assertIssueEditVersion(t, ctx, s, "beads/work", withReferences.Issue)
			assertIssueEditVersion(t, ctx, s, "beads/work", shortened.Issue)
			assertOverBudget()
			cleared, err := s.UpdateIssue(ctx, UpdateIssueRequest{
				Path: "beads/work", Actor: "clear", ExpectedRevision: shortened.Issue.Revision,
				ExternalRef: issueEditString(""), SpecID: issueEditString(""),
			})
			if err != nil || !cleared.Changed {
				t.Fatalf("guarded clear must not require current-read acquisition: changed=%t err=%v", cleared.Changed, err)
			}
			assertIssueReferencesTransition(t, shortened.Issue, cleared.Issue, nil, "", original.Properties.Title, "clear")
			assertIssueEditVersion(t, ctx, s, "beads/work", shortened.Issue)
			assertIssueEditVersion(t, ctx, s, "beads/work", cleared.Issue)
			assertIssueEditCounts(t, ctx, s, original.Properties.ID, 5)
			// The filler still exceeds the budget. Successful reference reduction
			// is not a claim to repair unrelated Memory growth or admit a full read.
			assertOverBudget()
			state := reopenState(t, ctx, s)
			for _, table := range []string{"dependencies", "graph_preview_versions", "graph_preview_links", "graph_preview_payloads"} {
				if state[table] != beforeEdits[table] {
					t.Fatalf("reference edits changed unrelated state in %s", table)
				}
			}
			for _, guarded := range []bool{true, false} {
				name := "unconditional-mixed-append"
				if guarded {
					name = "guarded-mixed-append"
				}
				if !t.Run(name, func(t *testing.T) {
					request := UpdateIssueRequest{
						Path: "beads/work", Actor: "mixed-refusal", Unconditional: !guarded,
						ExternalRef: issueEditString("must roll back"), SpecID: issueEditString("must also roll back"),
						AppendNotes: issueEditString("Small append must retain its budget postcondition"),
					}
					if guarded {
						request.ExpectedRevision = cleared.Issue.Revision
					}
					got, err := s.UpdateIssue(ctx, request)
					if !errors.Is(err, ErrLimitExceeded) || !reflect.DeepEqual(got, IssueMutationResult{}) {
						t.Fatalf("mixed append must refuse with zero result: changed=%t err=%v", got.Changed, err)
					}
					// Includes live notes/references, row tokens, events, leases,
					// retained versions, graph mappings and the unrelated Memory.
					if !reflect.DeepEqual(state, reopenState(t, ctx, s)) {
						t.Fatal("mixed reference/append refusal leaked state")
					}
					assertIssueEditCounts(t, ctx, s, original.Properties.ID, 5)
					assertIssueEditVersion(t, ctx, s, "beads/work", cleared.Issue)
					if current, err := s.ShowIssue(ctx, "beads/work"); err != nil || !reflect.DeepEqual(current, cleared.Issue) {
						t.Fatalf("mixed refusal changed the authoritative complete Issue: %v", err)
					}
					assertOverBudget()
				}) {
					return // Preserve the first failure instead of making another append attempt.
				}
			}
			if got, err := s.ShowIssue(ctx, "beads/prereq"); err != nil || !reflect.DeepEqual(got, target) {
				t.Fatalf("reference operations changed prerequisite: %v", err)
			}
			if got, err := s.ShowLink(ctx, "links/block"); err != nil || !reflect.DeepEqual(got, dependency) {
				t.Fatalf("reference operations changed owned Dependency: %v", err)
			}
		})
	}
}
