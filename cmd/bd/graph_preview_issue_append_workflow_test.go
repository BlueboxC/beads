//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/storage/graphstore"
)

// Every write and current/exact read is a separate installed process. The
// inspection helper only reads the normally initialized workspace.
func TestGraphPreviewIssueAppendWorkflow(t *testing.T) {
	bd := buildBDUnderTest(t)
	for _, engine := range []string{"embedded", "server"} {
		t.Run(engine, func(t *testing.T) {
			work, home := t.TempDir(), t.TempDir()
			const scope = "https://example.invalid/append/"
			args := []string{"init", "--graph-mode", "link", "--scope-url", scope, "--skip-hooks", "--skip-agents", "--non-interactive"}
			if engine == "server" {
				port := os.Getenv("BEADS_GRAPH_TEST_SERVER_PORT")
				if port == "" {
					t.Skip("set BEADS_GRAPH_TEST_SERVER_PORT for ordinary shared-server append qualification")
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
			checkAppend := func(before, current graphstore.IssueRecord, notes string) {
				t.Helper()
				if current.Properties == nil || current.Properties.Notes != notes || current.Revision == before.Revision || current.ID != before.ID || current.Type != before.Type || current.Attribution.Actor != "holder" || !reflect.DeepEqual(current.Owned, before.Owned) {
					t.Fatalf("incomplete append: %+v", current)
				}
				properties := *current.Properties
				properties.Notes, properties.UpdatedAt = before.Properties.Notes, before.Properties.UpdatedAt
				properties.ContentHash, properties.RowVersion = before.Properties.ContentHash, before.Properties.RowVersion
				if !reflect.DeepEqual(properties, *before.Properties) {
					t.Fatal("notes-only append changed unrelated properties, lease or closure")
				}
				if got := graphMixedResult[graphstore.IssueRecord](t, call("show", current.ID)); !reflect.DeepEqual(got, current) {
					t.Fatal("fresh-process read differs from append")
				}
				exact(before)
				exact(current)
			}
			call(args...)
			memory := call("remember", "Progress context", "--id", "beads/context", "--title", "Context")
			call("create", "Record progress", "--id", "beads/work", "--labels", "demo,graph")
			target := call("create", "Prerequisite", "--id", "beads/gate")
			dependency := graphMixedResult[graphstore.DependencyResult](t, call("dep", "add", "beads/work", "beads/gate"))
			link := graphMixedResult[graphstore.LinkMutationResult](t, call("link", "beads/work", "beads/context", "--id", "links/context", "--resource-type", scope+"types/preview-related-v2"))
			original := graphMixedResult[graphstore.IssueRecord](t, call("show", "beads/work"))
			claimed := graphMixedResult[graphstore.IssueMutationResult](t, call("update", "beads/work", "--claim", "--actor", "holder"))
			if !claimed.Changed || claimed.Issue.Properties.Assignee != "holder" || claimed.Issue.Properties.LeaseExpiresAt == nil {
				t.Fatalf("claim: %+v", claimed)
			}
			current := claimed.Issue
			state := graphMemoryReadSnapshot(t, work)
			noop := graphMixedResult[graphstore.IssueMutationResult](t, call("update", "beads/work", "--append-notes=", "--if-revision", current.Revision, "--actor", "holder"))
			if noop.Changed || !reflect.DeepEqual(noop.Issue, current) || !reflect.DeepEqual(state, graphMemoryReadSnapshot(t, work)) {
				t.Fatal("empty-on-empty append changed state")
			}
			notes := ""
			for _, text := range []string{"  Progress — 雪\r\nnext\t  ", "", "-", "@missing-notes.txt", "repeat", "repeat"} {
				before := current
				changed := graphMixedResult[graphstore.IssueMutationResult](t, call("update", "beads/work", "--append-notes", text, "--if-revision", current.Revision, "--actor", "holder"))
				if !changed.Changed {
					t.Fatal("nonempty-state append was treated as no-op")
				}
				if notes != "" {
					notes += "\n"
				}
				notes += text
				current = changed.Issue
				checkAppend(before, current, notes)
			}
			state = graphMemoryReadSnapshot(t, work)
			refuse("revision_conflict", "update", "beads/work", "--append-notes=", "--if-revision", original.Revision)
			refuse("permission_denied", "update", "beads/work", "--append-notes=No", "--readonly")
			refuse("invalid_properties", "update", "beads/work", "--append-notes=No", "--notes=Replacement")
			refuse("invalid_properties", "update", "beads/work", "--append-notes=No", "--notes=")
			refuse("capability_unavailable", "update", "beads/work", "--append-notes=No", "--claim")
			refuse("invalid_properties", "update", "beads/work", "--append-notes=No", "--claim=false")
			refuse("constraint_violation", "update", "beads/work", "--append-notes=No", "--assignee=thief", "--actor=thief", "--if-revision", current.Revision)
			refuse("capability_unavailable", "update", "beads/context", "--append-notes=No")
			refuse("capability_unavailable", "update", "links/context", "--append-notes=No")
			writeFile(t, filepath.Join(work, "mayor", "town.json"), []byte("{}\n"))
			freeze := filepath.Join(work, "MIGRATION-FREEZE")
			writeFile(t, freeze, []byte("append-test\t2026-09-29T00:00:00Z\tfreeze\n"))
			refuse("permission_denied", "update", "beads/work", "--append-notes=No")
			if err := os.Remove(freeze); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(state, graphMemoryReadSnapshot(t, work)) {
				t.Fatal("refusals changed complete current state")
			}
			if call("show", "beads/context") != memory || call("show", "beads/gate") != target {
				t.Fatal("append changed surrounding Beads")
			}
			if got := graphMixedResult[graphstore.LinkRecord](t, call("show", dependency.Link.ID)); !reflect.DeepEqual(got, dependency.Link) {
				t.Fatal("append changed Dependency")
			}
			if got := graphMixedResult[graphstore.LinkRecord](t, call("show", link.Link.ID)); !reflect.DeepEqual(got, link.Link) {
				t.Fatal("append changed mixed Link")
			}
			exact(original)
			exact(claimed.Issue)
			if engine == "server" {
				before := current
				outcomes := graphMutationRacePair(t, bd, work, home, [2][]string{
					{"update", "beads/work", "--append-notes=first", "--actor=holder", "--if-revision", current.Revision, "--json"},
					{"update", "beads/work", "--append-notes=second", "--actor=holder", "--if-revision", current.Revision, "--json"},
				}, map[string]int{"revision_conflict": 4})
				winner := -1
				for i, outcome := range outcomes {
					if outcome.code == "" {
						if winner != -1 {
							t.Fatal("both guarded appenders won")
						}
						winner = i
					}
				}
				if winner < 0 {
					t.Fatal("neither guarded appender won")
				}
				changed := graphMixedResult[graphstore.IssueMutationResult](t, outcomes[winner].stdout)
				if !changed.Changed {
					t.Fatal("append winner reported no-op")
				}
				notes += "\n" + []string{"first", "second"}[winner]
				current = changed.Issue
				checkAppend(before, current, notes)
			}
			call("close", "beads/gate", "--reason", "Done", "--actor", "holder")
			call("close", "beads/work", "--reason", "Done", "--actor", "holder")
			closed := graphMixedResult[graphstore.IssueRecord](t, call("show", "beads/work"))
			changed := graphMixedResult[graphstore.IssueMutationResult](t, call("update", "beads/work", "--append-notes=Post-completion", "--if-revision", closed.Revision, "--actor", "holder"))
			if !changed.Changed {
				t.Fatal("closed append reported no-op")
			}
			checkAppend(closed, changed.Issue, notes+"\nPost-completion")
		})
	}
}
