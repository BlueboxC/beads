//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/graphstore"
)

func graphMixedResult[T any](t *testing.T, output string) T {
	t.Helper()
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("decode command output: %v\n%s", err, output)
	}
	var result T
	if _, raw := any(result).(json.RawMessage); raw {
		return any(envelope.Result).(T)
	}
	// Legacy workflow oracles use graphstore structs. Translate only the
	// carried attribution envelope back to its storage vocabulary before
	// decoding them; public-wire assertions use the unmodified raw output.
	projected, err := graphMixedStoredAttribution(envelope.Result)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(projected, &result); err != nil {
		t.Fatalf("decode command result: %v\n%s", err, output)
	}
	return result
}

func graphMixedStoredAttribution(raw json.RawMessage) (json.RawMessage, error) {
	value := strings.TrimSpace(string(raw))
	if value == "" {
		return raw, nil
	}
	switch value[0] {
	case '{':
		var members map[string]json.RawMessage
		if err := json.Unmarshal(raw, &members); err != nil {
			return nil, err
		}
		if attribution, ok := members["attribution"]; ok {
			var carried map[string]json.RawMessage
			if len(attribution) > 0 && attribution[0] == '{' && json.Unmarshal(attribution, &carried) == nil {
				if basis, ok := carried["basis"]; ok {
					if string(basis) == `"writer-supplied"` {
						carried["status"] = json.RawMessage(`"claimed"`)
					} else {
						carried["status"] = basis
					}
					delete(carried, "basis")
					members["attribution"], _ = json.Marshal(carried)
				}
			}
		}
		for key, member := range members {
			if key == "properties" || key == "metadata" || key == "attribution" {
				continue
			}
			child, err := graphMixedStoredAttribution(member)
			if err != nil {
				return nil, err
			}
			members[key] = child
		}
		return json.Marshal(members)
	case '[':
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, err
		}
		for i, item := range items {
			child, err := graphMixedStoredAttribution(item)
			if err != nil {
				return nil, err
			}
			items[i] = child
		}
		return json.Marshal(items)
	default:
		return raw, nil
	}
}

// One complete installed-command sequence keeps the transferred adapters honest.
// Every command runs in a new process; the required CI lane supplies the server.
func TestGraphPreviewMixedCoreWorkflow(t *testing.T) {
	bd := buildBDUnderTest(t)
	for _, engine := range []string{"embedded", "server"} {
		t.Run(engine, func(t *testing.T) {
			work, home := t.TempDir(), t.TempDir()
			const scope = "https://example.invalid/mixed/"
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
			refuse := func(code string, args ...string) {
				t.Helper()
				graphPolicyCLI(t, bd, work, home, nil, code, append(args, "--json")...)
			}
			call(args...)
			memory := graphMixedResult[graphstore.Record](t, call("remember", "Original body", "--id", "beads/plan", "--title", "Plan"))
			other := call("remember", "Independent body", "--id", "beads/context", "--title", "Context")
			call("create", "Ship the release", "--id", "beads/work", "--description", "Original Issue", "--priority", "1")
			gate := call("create", "Prerequisite", "--id", "beads/gate")

			selected := graphMixedResult[graphstore.MemoryMutationResult](t, call("remember", "--update", "beads/plan", "--title", "Selected title", "--if-revision", memory.Revision))
			if !selected.Changed || selected.Memory.Properties.Body != memory.Properties.Body || selected.Replaced != nil {
				t.Fatal("selected guarded update lost omitted body or claimed unconditional overwrite")
			}
			replaced := graphMixedResult[graphstore.MemoryMutationResult](t, call("update", "beads/plan", "--properties", `{"title":"Final plan","body":"Replaced body"}`))
			if !replaced.Changed || replaced.Replaced == nil || replaced.Memory.Revision == selected.Memory.Revision {
				t.Fatal("unconditional Memory replacement omitted predecessor disclosure")
			}
			before := call("show", "beads/plan")
			refuse("revision_conflict", "remember", "--update", "beads/plan", "--title", "Final plan", "--if-revision", memory.Revision)
			if call("show", "beads/plan") != before || call("show", "beads/context") != other || call("show", "beads/gate") != gate {
				t.Fatal("stale Memory update changed state")
			}

			related := scope + "types/preview-related-v2"
			first := graphMixedResult[graphstore.LinkMutationResult](t, call("link", "beads/plan", "beads/context", "--resource-type", related, "--id", "links/context", "--properties", `{"note":"first"}`, "--if-source-revision", replaced.Memory.Revision))
			current := graphMixedResult[graphstore.Record](t, call("show", "beads/plan"))
			call("link", "beads/plan", "beads/work", "--resource-type", related, "--id", "links/work", "--if-source-revision", current.Revision)
			issueBefore := call("show", "beads/work")
			call("link", "beads/work", "beads/context", "--resource-type", related, "--id", "links/unowned")
			if call("show", "beads/work") != issueBefore || call("show", "beads/context") != other {
				t.Fatal("unowned informational Link versioned an endpoint")
			}
			incident := graphMixedResult[[]graphstore.LinkRecord](t, call("links", "beads/plan"))
			if len(incident) != 2 {
				t.Fatalf("Memory incident Links = %d, want 2", len(incident))
			}
			current = graphMixedResult[graphstore.Record](t, call("show", "beads/plan"))
			changedLink := graphMixedResult[graphstore.LinkMutationResult](t, call("update", "links/context", "--properties", `{"note":"replaced"}`, "--if-revision", first.Link.Revision, "--if-source-revision", current.Revision))
			if !changedLink.Changed || changedLink.Link.Properties["note"] != "replaced" {
				t.Fatal("Link properties were not replaced")
			}
			before = call("show", "beads/plan")
			linkBefore := call("show", "links/context")
			refuse("revision_conflict", "update", "links/context", "--properties", `{"note":"stale"}`, "--if-revision", first.Link.Revision, "--unconditional-source")
			if call("show", "beads/plan") != before || call("show", "links/context") != linkBefore {
				t.Fatal("stale Link update changed Link or owning Memory")
			}

			// The blocking Type is Issue-only. Selecting it explicitly for a
			// Memory endpoint, as source or target, keeps the invalid_properties
			// code and the store's cause, adds the hint naming an informational
			// Type to use instead, and changes neither endpoint.
			const memoryEndpointHint = "blocking Link Type requires two live Issues; use an informational Type such as types/preview-related-v2 for Memory endpoints: "
			planBefore, workBefore := call("show", "beads/plan"), call("show", "beads/work")
			for _, endpoints := range [][2]string{{"beads/plan", "beads/work"}, {"beads/work", "beads/plan"}} {
				stdout, stderr, exit := graphVersionsProcess(t, bd, work, home, "link", endpoints[0], endpoints[1], "--link-type", "types/preview-blocks-v1", "--unconditional-source", "--json")
				var diagnostic struct {
					Code      string `json:"code"`
					Message   string `json:"message"`
					Retryable bool   `json:"retryable"`
				}
				if exit != 2 || stdout != "" || json.Unmarshal([]byte(stderr), &diagnostic) != nil || diagnostic.Code != "invalid_properties" || diagnostic.Retryable ||
					!strings.HasPrefix(diagnostic.Message, memoryEndpointHint) || !strings.Contains(diagnostic.Message, "operation requires a live durable Issue, not generic") {
					t.Fatalf("blocking Type with Memory endpoint %v: exit=%d stdout=%q stderr=%q", endpoints, exit, stdout, stderr)
				}
			}
			if call("show", "beads/plan") != planBefore || call("show", "beads/work") != workBefore {
				t.Fatal("refused blocking Link changed an endpoint")
			}

			dependency := graphMixedResult[graphstore.DependencyResult](t, call("dep", "add", "beads/work", "beads/gate"))
			readyContains := func(id string) bool {
				t.Helper()
				for _, issue := range graphMixedResult[[]graphstore.IssueRecord](t, call("ready")) {
					if issue.ID == scope+id {
						return true
					}
				}
				return false
			}
			if readyContains("beads/work") {
				t.Fatal("blocked Issue appeared ready")
			}
			call("close", "beads/gate", "--reason", "Done")
			if !readyContains("beads/work") {
				t.Fatal("closing prerequisite did not unblock Issue")
			}
			workIssue := graphMixedResult[graphstore.IssueRecord](t, call("show", "beads/work"))
			call("unlink", dependency.Link.ID, "--if-revision", dependency.Link.Revision, "--if-source-revision", workIssue.Revision)
			call("close", "beads/work", "--reason", "Done")
			call("reopen", "beads/work", "--reason", "One more change")
			workIssue = graphMixedResult[graphstore.IssueRecord](t, call("show", "beads/work"))
			edited := graphMixedResult[graphstore.IssueMutationResult](t, call("update", "beads/work", "--description", "Revised Issue", "--if-revision", workIssue.Revision))
			if !edited.Changed || edited.Issue.Properties.Description != "Revised Issue" {
				t.Fatal("Issue text edit was not retained")
			}
			refuse("revision_conflict", "update", "beads/work", "--description", "Revised Issue", "--if-revision", workIssue.Revision)
			if got := graphMixedResult[graphstore.IssueRecord](t, call("show", "beads/work")); !reflect.DeepEqual(got, edited.Issue) {
				t.Fatal("stale same-value Issue update changed state")
			}

			for _, linkPath := range []string{"links/context", "links/work", "links/unowned"} {
				link := graphMixedResult[graphstore.LinkRecord](t, call("show", linkPath))
				args := []string{"unlink", linkPath, "--if-revision", link.Revision}
				if linkPath != "links/unowned" {
					owner := graphMixedResult[graphstore.Record](t, call("show", "beads/plan"))
					args = append(args, "--if-source-revision", owner.Revision)
				}
				call(args...)
				refuse("gone", "show", linkPath)
			}
			if links := graphMixedResult[[]graphstore.LinkRecord](t, call("links", "beads/plan")); len(links) != 0 {
				t.Fatal("unlinked Memory retained current incident Links")
			}
			final := graphMixedResult[graphstore.Record](t, call("show", scope+"beads/plan"))
			if final.Properties != replaced.Memory.Properties || len(final.Owned) != 0 || call("show", "beads/context") != other {
				t.Fatal("final fresh-process Memory state differs from the mixed workflow")
			}
		})
	}
}
