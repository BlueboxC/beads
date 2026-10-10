//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/graphstore"
)

// The query journey uses normal initialization and separate installed processes.
// Its authoring commands are already admitted; no SQL fixture or later writer
// is needed to demonstrate native blocked membership or generic traversal.
func TestGraphPreviewQueryWorkflow(t *testing.T) {
	bd := buildBDUnderTest(t)
	for _, engine := range []string{"embedded", "server"} {
		t.Run(engine, func(t *testing.T) {
			work, home := t.TempDir(), t.TempDir()
			const scope = "https://example.invalid/query/"
			args := []string{"init", "--graph-mode", "link", "--scope-url", scope, "--skip-hooks", "--skip-agents", "--non-interactive"}
			if engine == "server" {
				port := os.Getenv("BEADS_GRAPH_TEST_SERVER_PORT")
				if port == "" {
					t.Skip("set BEADS_GRAPH_TEST_SERVER_PORT for ordinary shared-server CLI qualification")
				}
				args = append(args, "--server", "--external", "--server-host", "127.0.0.1", "--server-port", port, "--server-user", "root")
			}
			call := func(args ...string) string {
				t.Helper()
				return graphPolicyCLI(t, bd, work, home, nil, "", append(args, "--json")...)
			}
			list := func(extra ...string) graphstore.IssueListPage {
				t.Helper()
				args := append([]string{"list", "--format", "records-json", "--bead-type", "types/preview-issue-v2"}, extra...)
				return graphMixedResult[graphstore.IssueListPage](t, graphPolicyCLI(t, bd, work, home, nil, "", args...))
			}
			beads := func(extra ...string) ([]graphListedBead, bool) {
				t.Helper()
				return graphListDecode(t, graphPolicyCLI(t, bd, work, home, nil, "", append([]string{"list", "--format", "records-json"}, extra...)...))
			}
			blocked := func() []graphstore.BlockedIssue {
				t.Helper()
				return graphMixedResult[[]graphstore.BlockedIssue](t, call("blocked"))
			}
			traverse := func(root, direction, depth string) graphGenericResult {
				t.Helper()
				output := call("graph", root, "--view", "generic", "--direction", direction, "--depth", depth)
				if strings.Contains(output, "private-memory-body") {
					t.Fatal("summary traversal exposed Memory body")
				}
				return graphMixedResult[graphGenericResult](t, output)
			}
			call(args...)
			call("remember", "private-memory-body", "--id", "beads/plan", "--title", "Plan")
			call("remember", "private-memory-body", "--id", "beads/context", "--title", "Context")
			call("create", "Work", "--id", "beads/work", "--priority", "1", "--labels", "release")
			call("create", "Gate", "--id", "beads/gate", "--priority", "2")
			related := scope + "types/preview-related-v2"
			for _, edge := range [][3]string{{"plan", "context", "context"}, {"plan", "work", "work"}, {"context", "gate", "gate"}} {
				call("link", "beads/"+edge[0], "beads/"+edge[1], "--id", "links/"+edge[2], "--resource-type", related, "--unconditional-source")
			}
			dep := graphMixedResult[graphstore.DependencyResult](t, call("dep", "add", "beads/work", "beads/gate"))
			graphPolicyCLI(t, bd, work, home, nil, "invalid_properties", "ready", "--label=", "--json")
			graphPolicyCLI(t, bd, work, home, nil, "invalid_properties", "blocked", "--label-any=", "--json")
			// The ordinary ready and blocked predicates now select canonical
			// graph Issues without changing their blocker-aware membership.
			if got := graphMixedResult[[]graphstore.BlockedIssue](t, call("blocked", "--label", "release")); len(got) != 1 || got[0].Issue.ID != scope+"beads/work" {
				t.Fatalf("blocked --label missed work: %+v", got)
			}
			if got := graphMixedResult[[]graphstore.BlockedIssue](t, call("blocked", "--exclude-label", "release")); len(got) != 0 {
				t.Fatalf("blocked --exclude-label leaked work: %+v", got)
			}
			if got := graphMixedResult[[]graphstore.IssueRecord](t, call("ready", "--priority", "2", "--type", "task")); len(got) != 1 || got[0].ID != scope+"beads/gate" {
				t.Fatalf("ready priority/type filter missed gate: %+v", got)
			}
			if got := graphMixedResult[[]graphstore.IssueRecord](t, call("ready", "--priority", "1")); len(got) != 0 {
				t.Fatalf("ready priority filter leaked blocked work: %+v", got)
			}
			// Complete the release journey in this same linked workspace before
			// the existing query, close/reopen, explicit unlink and delete phases.
			planBefore := graphMixedResult[graphstore.Record](t, call("show", "beads/plan"))
			workBefore := graphMixedResult[graphstore.IssueRecord](t, call("show", "beads/work"))
			unrelated := map[string]string{}
			for _, path := range []string{"beads/context", "beads/gate", "links/context", "links/work", "links/gate", dep.Link.ID} {
				unrelated[path] = call("show", path)
			}
			const replacement = `{"title":"Revised plan","body":"private-memory-body — revised"}`
			memoryEdit := graphMixedResult[graphstore.MemoryMutationResult](t, call("update", "beads/plan", "--properties", replacement, "--if-revision", planBefore.Revision))
			if !memoryEdit.Changed || memoryEdit.Replaced != nil || memoryEdit.Memory.ID != planBefore.ID || memoryEdit.Memory.Revision == planBefore.Revision || memoryEdit.Memory.Properties.Title != "Revised plan" || memoryEdit.Memory.Properties.Body != "private-memory-body — revised" || !reflect.DeepEqual(memoryEdit.Memory.Owned, planBefore.Owned) {
				t.Fatalf("linked Memory edit lost payload or owned Links: %+v", memoryEdit)
			}
			const description = "Revised Issue in the same Memory/Link journey."
			issueEdit := graphMixedResult[graphstore.IssueMutationResult](t, call("update", "beads/work", "--description", description, "--if-revision", workBefore.Revision))
			if !issueEdit.Changed || issueEdit.Issue.ID != workBefore.ID || issueEdit.Issue.Revision == workBefore.Revision || issueEdit.Issue.Properties.Description != description || !reflect.DeepEqual(issueEdit.Issue.Owned, workBefore.Owned) {
				t.Fatalf("Issue text edit lost payload or owned Dependencies: %+v", issueEdit)
			}
			if got := graphMixedResult[graphstore.Record](t, call("show", "beads/plan")); !reflect.DeepEqual(got, memoryEdit.Memory) {
				t.Fatal("fresh-process Memory differs from accepted edit")
			}
			if got := graphMixedResult[graphstore.IssueRecord](t, call("show", "beads/work")); !reflect.DeepEqual(got, issueEdit.Issue) {
				t.Fatal("fresh-process Issue differs from accepted edit")
			}
			for path, want := range unrelated {
				if got := call("show", path); got != want {
					t.Fatalf("scalar edits changed unrelated endpoint/Link %s", path)
				}
			}
			// Exact reads normalize only the documented current-only Issue fields.
			// Save the actual bytes to recheck after later unlink and deletion.
			type retainedRead struct{ id, version, output string }
			var retained []retainedRead
			for _, record := range []graphstore.Record{planBefore, memoryEdit.Memory} {
				output := call("show", record.ID, "--version", record.Revision)
				if got := graphMixedResult[graphstore.Record](t, output); !reflect.DeepEqual(got, record) {
					t.Fatalf("Memory predecessor/current retention: %+v want=%+v", got, record)
				}
				retained = append(retained, retainedRead{record.ID, record.Revision, output})
			}
			for _, record := range []graphstore.IssueRecord{workBefore, issueEdit.Issue} {
				properties := *record.Properties
				properties.ContentHash, properties.RowVersion = "", 0
				record.Properties = &properties
				output := call("show", record.ID, "--version", record.Revision)
				if got := graphMixedResult[graphstore.IssueRecord](t, output); !reflect.DeepEqual(got, record) {
					t.Fatalf("Issue predecessor/current retention: %+v want=%+v", got, record)
				}
				retained = append(retained, retainedRead{record.ID, record.Revision, output})
			}
			stable := graphMemoryReadSnapshot(t, work)
			memoryNoop := graphMixedResult[graphstore.MemoryMutationResult](t, call("update", "beads/plan", "--properties", replacement, "--if-revision", memoryEdit.Memory.Revision))
			issueNoop := graphMixedResult[graphstore.IssueMutationResult](t, call("update", "beads/work", "--description", description, "--if-revision", issueEdit.Issue.Revision))
			if memoryNoop.Changed || memoryNoop.Replaced != nil || !reflect.DeepEqual(memoryNoop.Memory, memoryEdit.Memory) || issueNoop.Changed || !reflect.DeepEqual(issueNoop.Issue, issueEdit.Issue) {
				t.Fatal("same-value release edits were not no-ops")
			}
			graphPolicyCLI(t, bd, work, home, nil, "revision_conflict", "update", "beads/plan", "--properties", replacement, "--if-revision", planBefore.Revision, "--json")
			graphPolicyCLI(t, bd, work, home, nil, "revision_conflict", "update", "beads/work", "--description", description, "--if-revision", workBefore.Revision, "--json")
			if !reflect.DeepEqual(stable, graphMemoryReadSnapshot(t, work)) {
				t.Fatal("no-op or stale edit changed the whole current snapshot")
			}
			// The inventory lists the newest recorded change first, so its order is
			// checked against the records' own recorded times, not their IDs.
			sortedIDs := func(items []graphListedBead) []string {
				ids := []string{}
				for _, item := range items {
					ids = append(ids, item.ID)
				}
				slices.Sort(ids)
				return ids
			}
			allBeads, more := beads("--all")
			if more || len(allBeads) != 4 || !graphListNewestFirst(t, allBeads) || !slices.Equal(sortedIDs(allBeads), []string{scope + "beads/context", scope + "beads/gate", scope + "beads/plan", scope + "beads/work"}) {
				t.Fatalf("complete mixed Bead inventory: %+v more=%t", allBeads, more)
			}
			firstPage, more := beads("--limit", "2")
			if !more || !reflect.DeepEqual(firstPage, allBeads[:2]) {
				t.Fatalf("bounded mixed Bead inventory: %+v more=%t", firstPage, more)
			}
			memories, more := beads("--bead-type", "types/preview-memory-v2", "--all")
			if more || len(memories) != 2 || !graphListNewestFirst(t, memories) || !slices.Equal(sortedIDs(memories), []string{scope + "beads/context", scope + "beads/plan"}) {
				t.Fatalf("Memory Type filter: %+v more=%t", memories, more)
			}
			if got := graphPolicyCLI(t, bd, work, home, nil, "", "list", "--all"); !strings.Contains(got, "Beads (4;") || !strings.Contains(got, "Memory") || !strings.Contains(got, "Issue") || strings.Contains(got, "private-memory-body") {
				t.Fatalf("mixed human inventory omitted kind or leaked Memory body: %s", got)
			}
			graphPolicyCLI(t, bd, work, home, nil, "capability_unavailable", "list", "--bead-type", "types/preview-related-v2", "--format", "records-json")
			graphPolicyCLI(t, bd, work, home, nil, "capability_unavailable", "list", "--bead-type", "types/uninstalled", "--format", "records-json")
			graphPolicyCLI(t, bd, work, home, nil, "capability_unavailable", "list", "--bead-type", "types/preview-memory-v2", "--status", "open", "--format", "records-json")

			page := list("--limit", "1", "--sort", "priority")
			if len(page.Items) != 1 || page.Items[0].ID != scope+"beads/work" || !page.HasMore {
				t.Fatalf("bounded list lost native order/over-fetch indication: %+v", page)
			}
			all := list("--all", "--sort", "priority")
			if len(all.Items) != 2 || all.HasMore || all.Items[1].ID != scope+"beads/gate" {
				t.Fatalf("complete Issue list: %+v", all)
			}
			filtered := list("--label", "release", "--status", "open")
			if len(filtered.Items) != 1 || filtered.Items[0].ID != scope+"beads/work" || filtered.HasMore {
				t.Fatalf("native label/status selection: %+v", filtered)
			}
			assertBlocked := func(want bool) {
				t.Helper()
				got := blocked()
				if !want && len(got) == 0 {
					return
				}
				if !want || len(got) != 1 || got[0].Issue.ID != scope+"beads/work" || !reflect.DeepEqual(got[0].BlockedBy, []string{scope + "beads/gate"}) {
					t.Fatalf("blocked membership want=%t got=%+v", want, got)
				}
			}
			assertReady := func(workReady, gateReady bool) {
				t.Helper()
				got := map[string]bool{}
				for _, item := range graphMixedResult[[]graphstore.IssueRecord](t, call("ready")) {
					got[item.ID] = true
				}
				want := map[string]bool{}
				if workReady {
					want[scope+"beads/work"] = true
				}
				if gateReady {
					want[scope+"beads/gate"] = true
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("ready=%v want=%v", got, want)
				}
			}
			assertBlocked(true)
			assertReady(false, true)
			whole := traverse("beads/plan", "out", "10")
			if len(whole.Nodes) != 4 || len(whole.Links) != 4 || !whole.Complete || len(whole.Frontier) != 0 {
				t.Fatalf("diamond must retain both routes and all Link identities: %+v", whole)
			}
			wantNodes := []string{scope + "beads/context", scope + "beads/gate", scope + "beads/plan", scope + "beads/work"}
			gotNodes := []string{}
			for _, node := range whole.Nodes {
				gotNodes = append(gotNodes, node.ID)
			}
			if !reflect.DeepEqual(gotNodes, wantNodes) {
				t.Fatalf("diamond nodes=%v want=%v", gotNodes, wantNodes)
			}
			wantLinks := map[string][2]string{
				scope + "links/context": {scope + "beads/plan", scope + "beads/context"},
				scope + "links/work":    {scope + "beads/plan", scope + "beads/work"},
				scope + "links/gate":    {scope + "beads/context", scope + "beads/gate"},
				dep.Link.ID:             {scope + "beads/work", scope + "beads/gate"},
			}
			gotLinks := map[string][2]string{}
			for _, link := range whole.Links {
				gotLinks[link.ID] = [2]string{link.Source, link.Target}
			}
			if !reflect.DeepEqual(gotLinks, wantLinks) {
				t.Fatalf("diamond links=%v want=%v", gotLinks, wantLinks)
			}
			for _, edge := range [][2]string{{"beads/plan", "in"}, {"beads/gate", "out"}} {
				one := traverse(edge[0], edge[1], "10")
				if len(one.Nodes) != 1 || one.Nodes[0].ID != scope+edge[0] || len(one.Links) != 0 || !one.Complete {
					t.Fatalf("direction ignored: %+v", one)
				}
			}
			inbound := traverse("beads/gate", "in", "10")
			if len(inbound.Nodes) != 4 || len(inbound.Links) != 4 || !inbound.Complete {
				t.Fatalf("inbound traversal: %+v", inbound)
			}
			frontier := traverse("beads/plan", "out", "0")
			if len(frontier.Nodes) != 1 || len(frontier.Links) != 0 || frontier.Complete || !reflect.DeepEqual(frontier.Frontier, []string{scope + "beads/plan"}) {
				t.Fatalf("depth frontier: %+v", frontier)
			}
			before := map[string]string{}
			for _, path := range []string{"beads/plan", "beads/context", "beads/work", "beads/gate", "links/context", "links/work", "links/gate"} {
				before[path] = call("show", path)
			}
			before[dep.Link.ID] = call("show", dep.Link.ID)
			graphPolicyCLI(t, bd, work, home, nil, "capability_unavailable", "graph", "beads/plan", "--view", "generic", "--depth", "10", "--max-nodes", "2", "--json")
			graphPolicyCLI(t, bd, work, home, nil, "capability_unavailable", "list", "--json")
			graphPolicyCLI(t, bd, work, home, nil, "capability_unavailable", "list", "--format", "records-json", "--watch")
			graphPolicyCLI(t, bd, work, home, nil, "invalid_selector", "graph", "https://foreign.invalid/beads/plan", "--view", "generic", "--json")
			normalList, normalBlocked := list("--all"), blocked()
			if got := list("--all", "--readonly"); !reflect.DeepEqual(got, normalList) {
				t.Fatalf("readonly list differs: %+v", got)
			}
			if got := graphMixedResult[[]graphstore.BlockedIssue](t, call("blocked", "--readonly")); !reflect.DeepEqual(got, normalBlocked) {
				t.Fatalf("readonly blocked differs: %+v", got)
			}
			if got := graphMixedResult[graphGenericResult](t, call("graph", "beads/plan", "--view", "generic", "--direction", "out", "--depth", "10", "--readonly")); !reflect.DeepEqual(got, whole) {
				t.Fatalf("readonly graph differs: %+v", got)
			}
			writeFile(t, filepath.Join(work, "mayor", "town.json"), []byte("{}\n"))
			freeze := filepath.Join(work, "MIGRATION-FREEZE")
			writeFile(t, freeze, []byte("query-test\t2026-09-28T00:00:00Z\tread policy\n"))
			if got := list("--all"); !reflect.DeepEqual(got, normalList) {
				t.Fatalf("frozen list differs: %+v", got)
			}
			if got := blocked(); !reflect.DeepEqual(got, normalBlocked) {
				t.Fatalf("frozen blocked differs: %+v", got)
			}
			if got := traverse("beads/plan", "out", "10"); !reflect.DeepEqual(got, whole) {
				t.Fatalf("frozen graph differs: %+v", got)
			}
			if err := os.Remove(freeze); err != nil {
				t.Fatal(err)
			}
			for path, expected := range before {
				if got := call("show", path); got != expected {
					t.Fatalf("query/refusal changed %s", path)
				}
			}
			call("close", "beads/gate")
			assertBlocked(false)
			assertReady(true, false)
			call("reopen", "beads/gate")
			assertBlocked(true)
			assertReady(false, true)
			source := graphMixedResult[graphstore.IssueRecord](t, call("show", "beads/work"))
			call("unlink", dep.Link.ID, "--if-revision", dep.Link.Revision, "--if-source-revision", source.Revision)
			assertBlocked(false)
			assertReady(true, true)
			for _, path := range []string{"links/context", "links/work"} {
				link := graphMixedResult[graphstore.LinkRecord](t, call("show", path))
				memory := graphMixedResult[graphstore.Record](t, call("show", "beads/plan"))
				call("unlink", path, "--if-revision", link.Revision, "--if-source-revision", memory.Revision)
			}
			memory := graphMixedResult[graphstore.Record](t, call("show", "beads/plan"))
			deleted := graphMixedResult[graphstore.MemoryDeleteResult](t, call("delete", "beads/plan", "--force", "--if-revision", memory.Revision))
			if !deleted.Deleted || deleted.Preview || !reflect.DeepEqual(deleted.Memory, memory) || memory.Properties != memoryEdit.Memory.Properties || len(memory.Owned) != 0 {
				t.Fatal("explicit unlink/delete lost the edited final Memory predecessor")
			}
			for _, saved := range retained {
				if got := call("show", saved.id, "--version", saved.version); got != saved.output {
					t.Fatalf("release lifecycle changed retained record %s@%s", saved.id, saved.version)
				}
			}
			if got := list("--all"); len(got.Items) != 2 || got.HasMore {
				t.Fatalf("valid deleted Memory invalidated surviving Issue view: %+v", got)
			}
			assertBlocked(false)
			after := traverse("beads/context", "both", "10")
			if len(after.Nodes) != 2 || len(after.Links) != 1 || after.Links[0].ID != scope+"links/gate" || !after.Complete {
				t.Fatalf("post-delete traversal: %+v", after)
			}
			graphPolicyCLI(t, bd, work, home, nil, "not_found", "graph", "beads/plan", "--view", "generic", "--json")
		})
	}
}
