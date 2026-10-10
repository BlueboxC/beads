//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage/graphstore"
)

// Author all state using installed commands. Every call is a separate process;
// inspection is read-only and never supplies fixture rows or bootstrap schema.
func TestGraphPreviewIssueAssignmentWorkflow(t *testing.T) {
	bd := buildBDUnderTest(t)
	for _, engine := range []string{"embedded", "server"} {
		t.Run(engine, func(t *testing.T) {
			work, home := t.TempDir(), t.TempDir()
			const scope = "https://example.invalid/assignment/"
			args := []string{"init", "--graph-mode", "link", "--scope-url", scope, "--skip-hooks", "--skip-agents", "--non-interactive"}
			if engine == "server" {
				port := os.Getenv("BEADS_GRAPH_TEST_SERVER_PORT")
				if port == "" {
					t.Skip("set BEADS_GRAPH_TEST_SERVER_PORT for ordinary shared-server assignment qualification")
				}
				args = append(args, "--server", "--external", "--server-host", "127.0.0.1", "--server-port", port, "--server-user", "root")
			}
			call := func(args ...string) string {
				t.Helper()
				return graphPolicyCLI(t, bd, work, home, nil, "", append(args, "--json")...)
			}
			refuse := func(code string, args ...string) {
				t.Helper()
				graphPolicyCLI(t, bd, work, home, nil, code, append(args, "--json")...)
			}
			list := func(want []string, extra ...string) {
				t.Helper()
				args := append([]string{"list", "--format", "records-json", "--bead-type", "types/preview-issue-v2", "--all"}, extra...)
				page := graphMixedResult[graphstore.IssueListPage](t, graphPolicyCLI(t, bd, work, home, nil, "", args...))
				got := make([]string, 0, len(page.Items))
				for _, item := range page.Items {
					got = append(got, item.ID)
				}
				slices.Sort(got)
				if page.Items == nil || page.HasMore || !slices.Equal(got, want) {
					t.Fatalf("list %v: ids=%v more=%v want=%v", extra, got, page.HasMore, want)
				}
			}
			exact := func(record graphstore.IssueRecord) {
				t.Helper()
				got := graphMixedResult[graphstore.IssueRecord](t, call("show", record.ID, "--version", record.Revision))
				properties := *record.Properties
				properties.ContentHash, properties.RowVersion = "", 0
				record.Properties = &properties
				if !reflect.DeepEqual(got, record) {
					t.Fatalf("exact retained Issue differs: got=%+v want=%+v", got, record)
				}
			}
			call(args...)
			memory := call("remember", "Assignment context", "--id", "beads/context", "--title", "Context")
			call("create", "Prioritize work", "--id", "beads/work", "--description", "Untouched description")
			gate := call("create", "Prerequisite", "--id", "beads/gate")
			dependency := graphMixedResult[graphstore.DependencyResult](t, call("dep", "add", "beads/work", "beads/gate"))
			informational := call("link", "beads/work", "beads/context", "--id", "links/context", "--resource-type", scope+"types/preview-related-v2")
			original := graphMixedResult[graphstore.IssueRecord](t, call("show", "beads/work"))
			edited := graphMixedResult[graphstore.IssueMutationResult](t, call("update", "beads/work", "--priority", "P0", "--assignee", "alice", "--title", "Assigned work", "--if-revision", original.Revision))
			current := edited.Issue
			if !edited.Changed || current.Properties.Priority != 0 || current.Properties.Assignee != "alice" || current.Properties.Title != "Assigned work" || current.Properties.Description != original.Properties.Description || current.Properties.Status != original.Properties.Status || current.Revision == original.Revision || !reflect.DeepEqual(current.Owned, original.Owned) {
				t.Fatalf("combined edit: %+v", edited)
			}
			if got := graphMixedResult[graphstore.IssueRecord](t, call("show", current.ID)); !reflect.DeepEqual(got, current) {
				t.Fatal("fresh-process current read differs")
			}
			exact(original)
			exact(current)
			list([]string{current.ID}, "--assignee=alice")
			list([]string{scope + "beads/gate"}, "--no-assignee")
			list([]string{}, "--assignee=alice", "--no-assignee")
			before := graphMemoryReadSnapshot(t, work)
			noop := graphMixedResult[graphstore.IssueMutationResult](t, call("update", "beads/work", "--priority", "0", "--assignee", "alice", "--if-revision", current.Revision))
			if noop.Changed || !reflect.DeepEqual(noop.Issue, current) {
				t.Fatal("same-value edit was not a semantic no-op")
			}
			refuse("revision_conflict", "update", "beads/work", "--priority", "0", "--assignee", "alice", "--if-revision", original.Revision)
			refuse("permission_denied", "update", "beads/work", "--priority", "1", "--readonly")
			refuse("capability_unavailable", "update", "beads/work", "--assignee", "bob", "--claim")
			refuse("capability_unavailable", "update", "beads/context", "--priority", "1")
			refuse("capability_unavailable", "update", "links/context", "--assignee", "bob")
			// A freeze must block this newly admitted edit before its writer opens.
			writeFile(t, filepath.Join(work, "mayor", "town.json"), []byte("{}\n"))
			freeze := filepath.Join(work, "MIGRATION-FREEZE")
			writeFile(t, freeze, []byte("assignment-test\t2026-09-28T00:00:00Z\tfreeze\n"))
			refuse("permission_denied", "update", "beads/work", "--assignee", "bob")
			if err := os.Remove(freeze); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, graphMemoryReadSnapshot(t, work)) {
				t.Fatal("no-op or refusal changed current graph state")
			}
			exact(original)
			exact(current)
			omitted := graphMixedResult[graphstore.IssueMutationResult](t, call("update", "beads/work", "--description", "Revised description", "--if-revision", current.Revision))
			if !omitted.Changed || omitted.Issue.Properties.Priority != 0 || omitted.Issue.Properties.Assignee != "alice" {
				t.Fatal("omitted fields were changed")
			}
			current = omitted.Issue
			cleared := graphMixedResult[graphstore.IssueMutationResult](t, call("update", "beads/work", "--assignee=", "--if-revision", current.Revision))
			if !cleared.Changed || cleared.Issue.Properties.Assignee != "" || cleared.Issue.Properties.Status != current.Properties.Status || cleared.Issue.Properties.Priority != 0 {
				t.Fatal("assignee clear changed unrelated state")
			}
			current = cleared.Issue
			list([]string{}, "--assignee=alice")
			list([]string{scope + "beads/gate", scope + "beads/work"}, "--no-assignee")
			if call("show", "beads/context") != memory || call("show", "beads/gate") != gate {
				t.Fatal("assignment changed unrelated Beads")
			}
			if got := graphMixedResult[graphstore.LinkRecord](t, call("show", dependency.Link.ID)); !reflect.DeepEqual(got, dependency.Link) {
				t.Fatal("assignment changed blocking Dependency")
			}
			if got := graphMixedResult[graphstore.LinkRecord](t, call("show", "links/context")); !reflect.DeepEqual(got, graphMixedResult[graphstore.LinkMutationResult](t, informational).Link) {
				t.Fatal("assignment changed informational Link")
			}
			// Independent server processes compete against one observed graph revision.
			// The storage tests force transaction overlap; this boundary proves actual
			// installed callers and refuses generic/infrastructure errors as losers.
			if engine == "server" {
				results := graphDeleteRacePair(t, bd, work, home, [2][]string{
					{"update", "beads/work", "--priority=1", "--assignee=first", "--if-revision", current.Revision, "--json"},
					{"update", "beads/work", "--priority=4", "--assignee=second", "--if-revision", current.Revision, "--json"},
				})
				winner := -1
				for i, result := range results {
					if result.code == "" {
						if winner != -1 {
							t.Fatal("both guarded callers committed")
						}
						winner = i
					} else if result.code != "revision_conflict" {
						t.Fatalf("unexpected losing outcome: %s", result.code)
					}
				}
				if winner < 0 {
					t.Fatal("neither guarded caller committed")
				}
				won := graphMixedResult[graphstore.IssueMutationResult](t, results[winner].stdout)
				wantPriority, wantAssignee := 1, "first"
				if winner == 1 {
					wantPriority, wantAssignee = 4, "second"
				}
				if !won.Changed || won.Issue.Properties.Priority != wantPriority || won.Issue.Properties.Assignee != wantAssignee || won.Issue.Revision == current.Revision || !reflect.DeepEqual(won.Issue.Owned, current.Owned) {
					t.Fatalf("torn/wrong winner: %+v", won)
				}
				if got := graphMixedResult[graphstore.IssueRecord](t, call("show", "beads/work")); !reflect.DeepEqual(got, won.Issue) {
					t.Fatal("fresh process did not read exact winner")
				}
				exact(current)
				current = won.Issue
				exact(current)
			}
			// Closed scalar edits must preserve the native closure, not reopen work.
			call("close", "beads/gate", "--reason", "Done")
			closed := graphMixedResult[graphstore.IssueRecord](t, call("show", "beads/gate"))
			afterClose := graphMixedResult[graphstore.IssueMutationResult](t, call("update", "beads/gate", "--priority=4", "--assignee=reviewer", "--if-revision", closed.Revision))
			if !afterClose.Changed || afterClose.Issue.Properties.Status != closed.Properties.Status || !reflect.DeepEqual(afterClose.Issue.Properties.ClosedAt, closed.Properties.ClosedAt) || afterClose.Issue.Properties.CloseReason != closed.Properties.CloseReason {
				t.Fatal("closed Issue edit changed closure")
			}
			exact(closed)
			exact(afterClose.Issue)
		})
	}
}
