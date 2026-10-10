//go:build cgo

package main

import (
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/graphstore"
	"github.com/steveyegge/beads/internal/types"
)

// A fresh installed graph workspace exercises filtered selection, the native
// ready claim and its retained graph projection on both supported engines.
func TestGraphPreviewReadyClaimWorkflow(t *testing.T) {
	bd := buildBDUnderTest(t)
	for _, engine := range []string{"embedded", "server"} {
		t.Run(engine, func(t *testing.T) {
			work, home := t.TempDir(), t.TempDir()
			const scope = "https://example.invalid/ready-claim/"
			initArgs := []string{"init", "--graph-mode", "link", "--scope-url", scope, "--skip-hooks", "--skip-agents", "--non-interactive"}
			if engine == "server" {
				port := os.Getenv("BEADS_GRAPH_TEST_SERVER_PORT")
				if port == "" {
					t.Skip("set BEADS_GRAPH_TEST_SERVER_PORT for ordinary shared-server qualification")
				}
				initArgs = append(initArgs, "--server", "--external", "--server-host", "127.0.0.1", "--server-port", port, "--server-user", "root")
			}
			call := func(args ...string) string {
				t.Helper()
				return graphPolicyCLI(t, bd, work, home, nil, "", append(args, "--json")...)
			}
			call(initArgs...)
			call("create", "Release task", "--id", "release", "--priority", "1", "--labels", "release")
			call("create", "Docs task", "--id", "docs", "--priority", "2", "--labels", "docs")
			call("create", "Blocked task", "--id", "blocked", "--priority", "0", "--labels", "release")
			call("dep", "add", "blocked", "docs")
			before := graphMixedResult[graphstore.IssueRecord](t, call("show", "release"))
			if got := graphMixedResult[[]graphstore.IssueRecord](t, call("ready", "--label", "release")); len(got) != 1 || got[0].ID != before.ID {
				t.Fatalf("filtered ready front: %+v", got)
			}
			if got := graphMixedResult[[]graphstore.BlockedIssue](t, call("blocked", "--label", "release")); len(got) != 1 || got[0].Issue.ID != scope+"beads/blocked" {
				t.Fatalf("filtered blocked front: %+v", got)
			}
			claimed := graphMixedResult[[]graphstore.IssueRecord](t, call("ready", "--claim", "--label", "release", "--actor", "ready-agent"))
			if len(claimed) != 1 || claimed[0].ID != before.ID || claimed[0].Revision == before.Revision {
				t.Fatalf("filtered claim did not change exactly one Issue: %+v", claimed)
			}
			current := claimed[0]
			if p := current.Properties; p == nil || p.Status != types.StatusInProgress || p.Assignee != "ready-agent" || p.LeaseExpiresAt == nil || p.HeartbeatAt == nil || p.LeaseExpiresAt.Sub(*p.HeartbeatAt) != 5*time.Minute {
				t.Fatalf("claimed Issue did not retain native lease: %+v", p)
			}
			if got := graphMixedResult[graphstore.IssueRecord](t, call("show", "release")); !reflect.DeepEqual(got, current) {
				t.Fatalf("fresh process did not see claimed record: %+v", got)
			}
			_, kind, versions, _ := graphVersionsListed(t, call("versions", "release"))
			if kind != "issue" || len(versions) != 2 || versions[0].Version != current.Revision || versions[1].Version != before.Revision {
				t.Fatalf("ready claim did not mint exactly one retained version: %+v", versions)
			}
			if got := graphMixedResult[[]graphstore.IssueRecord](t, call("ready", "--claim", "--label", "release", "--actor", "ready-agent")); len(got) != 0 {
				t.Fatalf("empty filtered front claimed work: %+v", got)
			}
			if got := graphMixedResult[[]graphstore.IssueRecord](t, call("ready", "--claim", "--priority", "0", "--actor", "ready-agent")); len(got) != 0 {
				t.Fatalf("blocked Issue was selected for claim: %+v", got)
			}
			if got := graphMixedResult[graphstore.IssueRecord](t, call("show", "release")); !reflect.DeepEqual(got, current) {
				t.Fatal("empty claims changed the existing lease or revision")
			}
			if _, _, again, _ := graphVersionsListed(t, call("versions", "release")); !reflect.DeepEqual(again, versions) {
				t.Fatal("empty claims added retained versions")
			}
			call("create", "Sleeping task", "--id", "sleeping", "--labels", "sleeping")
			sleeping := graphMixedResult[graphstore.IssueMutationResult](t, call("defer", "sleeping", "--until", "2000-01-01"))
			if sleeping.Issue.Properties.Status != types.StatusDeferred {
				t.Fatalf("setup did not defer sleeping Issue: %+v", sleeping)
			}
			if got := graphMixedResult[[]graphstore.IssueRecord](t, call("ready", "--claim", "--label", "missing", "--actor", "ready-agent")); len(got) != 0 {
				t.Fatalf("filtered empty claim unexpectedly selected work: %+v", got)
			}
			woken := graphMixedResult[graphstore.IssueRecord](t, call("show", "sleeping"))
			if woken.Revision == sleeping.Issue.Revision || woken.Properties.Status != types.StatusOpen || woken.Properties.DeferUntil != nil || woken.Properties.LeaseExpiresAt != nil {
				t.Fatalf("empty claim did not retain the separate native defer wake: %+v", woken)
			}
			if _, _, wakeVersions, _ := graphVersionsListed(t, call("versions", "sleeping")); len(wakeVersions) != 3 || wakeVersions[0].Version != woken.Revision {
				t.Fatalf("empty claim did not retain one defer-wake version: %+v", wakeVersions)
			}
			if engine == "server" {
				call("create", "Race task", "--id", "race", "--labels", "race")
				outcomes := graphMutationRacePair(t, bd, work, home, [2][]string{
					{"ready", "--claim", "--label", "race", "--actor", "first", "--json"},
					{"ready", "--claim", "--label", "race", "--actor", "second", "--json"},
				}, map[string]int{"revision_conflict": 4})
				winners := 0
				for _, outcome := range outcomes {
					if outcome.code != "" {
						continue
					}
					if got := graphMixedResult[[]graphstore.IssueRecord](t, outcome.stdout); len(got) == 1 && got[0].ID == scope+"beads/race" {
						winners++
					}
				}
				if winners != 1 {
					t.Fatalf("concurrent ready claim has %d winners: %+v", winners, outcomes)
				}
				if _, _, raceVersions, _ := graphVersionsListed(t, call("versions", "race")); len(raceVersions) != 2 {
					t.Fatalf("concurrent claim minted unexpected versions: %+v", raceVersions)
				}
			}
		})
	}
}
