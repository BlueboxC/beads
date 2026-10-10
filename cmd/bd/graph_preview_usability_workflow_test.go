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

func TestGraphPreviewUsabilityWorkflow(t *testing.T) {
	bd := buildBDUnderTest(t)
	for _, engine := range []string{"embedded", "server"} {
		t.Run(engine, func(t *testing.T) {
			work, home := t.TempDir(), t.TempDir()
			const scope = "https://example.invalid/usability/"
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
			var installed struct {
				Result struct {
					BeadTypes []json.RawMessage `json:"beadTypes"`
					LinkTypes []json.RawMessage `json:"linkTypes"`
				} `json:"result"`
			}
			if err := json.Unmarshal([]byte(call("types")), &installed); err != nil || len(installed.Result.BeadTypes) != 2 || len(installed.Result.LinkTypes) != 4 {
				t.Fatalf("installed graph Types unavailable: %+v %v", installed, err)
			}
			for _, item := range append(installed.Result.BeadTypes, installed.Result.LinkTypes...) {
				var descriptor struct{ ID, Describes string }
				if err := json.Unmarshal(item, &descriptor); err != nil || !strings.HasPrefix(descriptor.ID, scope+"types/") {
					t.Fatalf("invalid installed descriptor: %s %v", item, err)
				}
			}
			visible := graphPolicyCLI(t, bd, work, home, nil, "", "types")
			if !strings.Contains(visible, "Bead Types") || !strings.Contains(visible, "Link Types") || !strings.Contains(visible, "types/example-cites") {
				t.Fatalf("Type selection help missing: %s", visible)
			}
			detailsText := graphPolicyCLI(t, bd, work, home, nil, "", "types", "--details")
			if !strings.Contains(detailsText, `"describes": "link"`) || !strings.Contains(detailsText, `"source":`) || !strings.Contains(detailsText, `"target":`) {
				t.Fatalf("full Link descriptor unavailable: %s", detailsText)
			}
			refuse("capability_unavailable", "types", "--sections")
			memory := graphMixedResult[graphstore.Record](t, call("create", "--bead-type", "types/preview-memory-v2", "--id", "policy", "--body", "Code flow policy\nTarget integration."))
			other := graphMixedResult[graphstore.Record](t, call("create", "Old code flow policy", "--bead-type", graphstore.MemoryTypeURL(scope)))
			issue := graphMixedResult[graphstore.IssueRecord](t, call("create", "Move the branch", "--id", "work", "--bead-type", "types/preview-issue-v2", "--type", "task"))
			if memory.ID != scope+"beads/policy" || memory.Properties.Title != "Code flow policy" || other.ID == memory.ID || other.Properties.Body != "" || issue.ID != scope+"beads/work" || issue.Type != graphstore.IssueTypeURL(scope) {
				t.Fatal("typed creation lost identity, body or Type")
			}
			if shown := graphMixedResult[graphstore.Record](t, call("show", "policy")); shown.ID != memory.ID {
				t.Fatal("short Bead selector did not resolve to its canonical identity")
			}
			refuse("identity_reserved", "create", "No overwrite", "--bead-type", "types/preview-memory-v2", "--id", "beads/policy")
			refuse("capability_unavailable", "create", "Bad", "--bead-type", "types/preview-memory-v2", "--priority", "2")
			refuse("invalid_selector", "link", memory.ID, other.ID, "--link-type", "types/example-follows", "--resource-type", "types/example-follows")
			refuse("invalid_selector", "link", memory.ID, other.ID, "--link-type", "https://foreign.invalid/types/example-follows")
			refuse("capability_unavailable", "link", memory.ID, other.ID, "--link-type", "types/missing")
			refuse("invalid_properties", "link", memory.ID, other.ID, "--link-type", "types/preview-blocks-v1", "--unconditional-source")
			human := graphPolicyCLI(t, bd, work, home, nil, "", "memories")
			if !strings.Contains(human, "beads/policy") || !strings.Contains(human, "Code flow policy") || strings.Contains(human, scope) || strings.Contains(human, memory.Revision) {
				t.Fatalf("noisy Memory list: %s", human)
			}
			details := graphPolicyCLI(t, bd, work, home, nil, "", "memories", "--details")
			if !strings.Contains(details, memory.Revision) {
				t.Fatal("details omitted exact version")
			}
			for _, spec := range []struct{ typ, target string }{{"types/example-follows", other.ID}, {"types/example-cites", issue.ID}} {
				link := graphMixedResult[graphstore.LinkMutationResult](t, call("link", "policy", strings.TrimPrefix(spec.target, scope+"beads/"), "--link-type", spec.typ, "--properties", `{"note":"code flow"}`))
				if link.Link.Type != scope+spec.typ || link.Link.Source != memory.ID || link.Link.Target != spec.target || link.ReplacedSource == nil {
					t.Fatal("informational Link lost Type/endpoints/ownership")
				}
				owner := graphMixedResult[graphstore.Record](t, call("show", memory.ID))
				var owned graphstore.LinkRecord
				if len(owner.Owned) != 1 || json.Unmarshal(owner.Owned[0], &owned) != nil || owned.Type != scope+spec.typ {
					t.Fatal("selected Type not retained as owned")
				}
				incident := graphMixedResult[[]graphstore.LinkRecord](t, call("links", "policy", "--link-type", spec.typ))
				if len(incident) != 1 || incident[0].Type != scope+spec.typ {
					t.Fatal("local Type filter failed")
				}
				edited := graphMixedResult[graphstore.LinkMutationResult](t, call("update", link.Link.ID, "--properties", `{"note":"updated"}`, "--if-revision", link.Link.Revision))
				if edited.Link.Type != link.Link.Type {
					t.Fatal("update changed Type")
				}
				// Every installed informational Type lists its history, not only Related.
				resource, kind, versions, members := graphVersionsListed(t, call("versions", link.Link.ID))
				if resource != strings.TrimPrefix(link.Link.ID, scope) || kind != "link" || len(versions) != 2 ||
					versions[0].Version != edited.Link.Revision || versions[1].Version != link.Link.Revision {
					t.Fatalf("%s Link versions: resource=%q kind=%q rows=%+v", spec.typ, resource, kind, versions)
				}
				graphVersionsAssertKeys(t, spec.typ, members)
				old := graphMixedResult[graphstore.LinkRecord](t, call("show", link.Link.ID, "--version", link.Link.Revision))
				if !reflect.DeepEqual(old, link.Link) {
					t.Fatal("retained Link changed")
				}
				call("unlink", memory.ID, spec.target, "--link-type", scope+spec.typ, "--if-revision", edited.Link.Revision)
				retained := graphMixedResult[graphstore.Record](t, call("show", memory.ID, "--version", owner.Revision))
				if !reflect.DeepEqual(retained, owner) {
					t.Fatal("unlink rewrote old Memory-owned snapshot")
				}
			}
			// Legacy spelling remains accepted for existing scripts.
			call("link", memory.ID, other.ID, "--resource-type", "types/preview-related-v2")
			sameIDBead := graphMixedResult[graphstore.Record](t, call("create", "Same local ID", "--bead-type", "types/preview-memory-v2", "--id", "edge"))
			shortLink := graphMixedResult[graphstore.LinkMutationResult](t, call("link", memory.ID, other.ID, "--link-type", "types/example-follows", "--id", "edge"))
			if sameIDBead.ID != scope+"beads/edge" || shortLink.Link.ID != scope+"links/edge" {
				t.Fatal("bare Link ID was not scoped independently from the Bead ID")
			}
			refuse("invalid_selector", "link", memory.ID, other.ID, "--link-type", "types/example-follows", "--id", "beads/edge")
			call("unlink", "edge", "--if-revision", shortLink.Link.Revision)
			refuse("gone", "show", "links/edge")
			if shown := graphMixedResult[graphstore.Record](t, call("show", "edge")); shown.ID != sameIDBead.ID {
				t.Fatal("Link-only unlink selected the Bead with the same bare ID")
			}
			call("remember", "Updated code flow policy", "--update", "policy")
			current := graphMixedResult[graphstore.Record](t, call("show", "policy"))
			if current.ID != memory.ID || current.Properties.Body != "Updated code flow policy" {
				t.Fatal("short Memory update changed identity or lost its body")
			}
			call("compare", "policy", "--from", memory.Revision, "--to", current.Revision)
			ordinaryWork, ordinaryHome := t.TempDir(), t.TempDir()
			graphPolicyCLI(t, bd, ordinaryWork, ordinaryHome, nil, "capability_unavailable", "create", "--bead-type", "types/preview-memory-v2", "--body", "No store", "--json")
			graphPolicyCLI(t, bd, ordinaryWork, ordinaryHome, nil, "capability_unavailable", "list", "--bead-type", "types/preview-memory-v2", "--format", "records-json")
		})
	}
}
