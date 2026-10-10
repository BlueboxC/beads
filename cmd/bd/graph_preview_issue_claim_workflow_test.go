//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/graphstore"
	"github.com/steveyegge/beads/internal/types"
)

// All authoring uses normal installed CLI processes, including initialization.
// Read-only inspection never seeds schema, payloads, leases or graph mappings.
func TestGraphPreviewIssueClaimWorkflow(t *testing.T) {
	bd := buildBDUnderTest(t)
	for _, engine := range []string{"embedded", "server"} {
		t.Run(engine, func(t *testing.T) {
			work, home := t.TempDir(), t.TempDir()
			const scope = "https://example.invalid/claim/"
			args := []string{"init", "--graph-mode", "link", "--scope-url", scope, "--skip-hooks", "--skip-agents", "--non-interactive"}
			if engine == "server" {
				port := os.Getenv("BEADS_GRAPH_TEST_SERVER_PORT")
				if port == "" {
					t.Skip("set BEADS_GRAPH_TEST_SERVER_PORT for ordinary shared-server claim qualification")
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
			assertClaim := func(before, current graphstore.IssueRecord, actor string) {
				t.Helper()
				p := current.Properties
				if p == nil || p.Status != types.StatusInProgress || p.Assignee != actor || p.StartedAt == nil || p.HeartbeatAt == nil || p.LeaseExpiresAt == nil || p.LeaseExpiresAt.Sub(*p.HeartbeatAt) != 5*time.Minute || current.Revision == before.Revision || current.ID != before.ID || current.Type != before.Type || current.Attribution.Actor != actor {
					t.Fatalf("incomplete claim: %+v properties=%+v", current, p)
				}
				properties := *p
				properties.Status, properties.Assignee = before.Properties.Status, before.Properties.Assignee
				properties.StartedAt, properties.UpdatedAt = before.Properties.StartedAt, before.Properties.UpdatedAt
				properties.LeaseExpiresAt, properties.HeartbeatAt, properties.LeaseGrantedNode = before.Properties.LeaseExpiresAt, before.Properties.HeartbeatAt, before.Properties.LeaseGrantedNode
				properties.ContentHash, properties.RowVersion = before.Properties.ContentHash, before.Properties.RowVersion
				if !reflect.DeepEqual(properties, *before.Properties) || !reflect.DeepEqual(current.Owned, before.Owned) {
					t.Fatal("claim changed unrelated Issue properties or owned Links")
				}
				if got := graphMixedResult[graphstore.IssueRecord](t, call("show", current.ID)); !reflect.DeepEqual(got, current) {
					t.Fatal("fresh process did not read complete claim")
				}
				exact(before)
				exact(current)
			}
			call(args...)
			memory := call("remember", "Claim context", "--id", "beads/context", "--title", "Context")
			call("create", "Claim work", "--id", "beads/work", "--labels", "demo,graph", "--actor", "creator")
			target := call("create", "Prerequisite", "--id", "beads/gate")
			dependency := graphMixedResult[graphstore.DependencyResult](t, call("dep", "add", "beads/work", "beads/gate"))
			link := graphMixedResult[graphstore.LinkMutationResult](t, call("link", "beads/work", "beads/context", "--id", "links/context", "--resource-type", scope+"types/preview-related-v2"))
			before := graphMixedResult[graphstore.IssueRecord](t, call("show", "beads/work"))
			claimed := graphMixedResult[graphstore.IssueMutationResult](t, call("update", "beads/work", "--claim", "--actor", "rig.agent"))
			if !claimed.Changed {
				t.Fatal("initial claim did not change Issue")
			}
			current := claimed.Issue
			assertClaim(before, current, "rig.agent")
			state := graphMemoryReadSnapshot(t, work)
			for _, actor := range []string{"rig.agent", "rig_agent"} {
				noop := graphMixedResult[graphstore.IssueMutationResult](t, call("update", "beads/work", "--claim=true", "--actor", actor))
				if noop.Changed || !reflect.DeepEqual(noop.Issue, current) {
					t.Fatalf("same-holder claim renewed/changed state: %+v", noop)
				}
			}
			refuse("constraint_violation", "update", "beads/work", "--claim", "--actor", "foreign")
			refuse("invalid_properties", "update", "beads/work", "--claim=false", "--actor", "rig.agent")
			refuse("invalid_properties", "update", "beads/work", "--claim=false", "--priority=0")
			refuse("capability_unavailable", "update", "beads/work", "--claim", "--priority=0")
			refuse("capability_unavailable", "update", "beads/work", "--claim", "--assignee=other")
			refuse("capability_unavailable", "update", "beads/work", "--claim", "--if-revision", current.Revision)
			refuse("capability_unavailable", "update", "beads/work", "--claim", "--patch=[]")
			refuse("permission_denied", "update", "beads/work", "--claim", "--readonly")
			refuse("invalid_properties", "update", "beads/context", "--claim")
			refuse("invalid_selector", "update", "links/context", "--claim")
			writeFile(t, filepath.Join(work, "mayor", "town.json"), []byte("{}\n"))
			freeze := filepath.Join(work, "MIGRATION-FREEZE")
			writeFile(t, freeze, []byte("claim-test\t2026-09-28T00:00:00Z\tfreeze\n"))
			refuse("permission_denied", "update", "beads/work", "--claim")
			if err := os.Remove(freeze); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(state, graphMemoryReadSnapshot(t, work)) {
				t.Fatal("no-op/refusal changed current graph, including lease timestamps")
			}
			if call("show", "beads/context") != memory || call("show", "beads/gate") != target {
				t.Fatal("claim changed unrelated Beads")
			}
			if got := graphMixedResult[graphstore.LinkRecord](t, call("show", dependency.Link.ID)); !reflect.DeepEqual(got, dependency.Link) {
				t.Fatal("claim changed Dependency")
			}
			if got := graphMixedResult[graphstore.LinkRecord](t, call("show", link.Link.ID)); !reflect.DeepEqual(got, link.Link) {
				t.Fatal("claim changed informational Link")
			}
			exact(before)
			exact(current)
			// The existing storage test forces overlap. Independent installed server
			// processes also prove one complete winner and only typed domain refusals.
			if engine == "server" {
				raceBefore := graphMixedResult[graphstore.IssueRecord](t, call("create", "Race for claim", "--id", "beads/race"))
				outcomes := graphMutationRacePair(t, bd, work, home, [2][]string{
					{"update", "beads/race", "--claim", "--actor=first", "--json"},
					{"update", "beads/race", "--claim", "--actor=second", "--json"},
				}, map[string]int{"revision_conflict": 4, "constraint_violation": 4})
				winner := -1
				for i, outcome := range outcomes {
					if outcome.code == "" {
						if winner != -1 {
							t.Fatal("both claimants won")
						}
						winner = i
					}
				}
				if winner < 0 {
					t.Fatal("neither claimant won")
				}
				won := graphMixedResult[graphstore.IssueMutationResult](t, outcomes[winner].stdout)
				if !won.Changed {
					t.Fatal("race winner reported no-op")
				}
				assertClaim(raceBefore, won.Issue, []string{"first", "second"}[winner])
			}
		})
	}
}
