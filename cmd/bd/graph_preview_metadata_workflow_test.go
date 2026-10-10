//go:build cgo

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/graphstore"
)

func TestGraphPreviewCommonMetadataInstalledWorkflow(t *testing.T) {
	bd := buildBDUnderTest(t)
	for _, engine := range []string{"embedded", "server"} {
		t.Run(engine, func(t *testing.T) {
			work, home := t.TempDir(), t.TempDir()
			const scope = "https://example.invalid/metadata-workflow/"
			call := func(args ...string) string {
				t.Helper()
				return graphPatchProcess(t, bd, work, home, nil, "", 90*time.Second, append(args, "--json")...)
			}
			refuse := func(code string, args ...string) {
				t.Helper()
				graphPatchProcess(t, bd, work, home, nil, code, 90*time.Second, append(args, "--json")...)
			}
			init := []string{"init", "--graph-mode", "link", "--scope-url", scope, "--skip-hooks", "--skip-agents", "--non-interactive"}
			if engine == "server" {
				port := os.Getenv("BEADS_GRAPH_TEST_SERVER_PORT")
				if port == "" {
					t.Skip("set BEADS_GRAPH_TEST_SERVER_PORT for required ordinary shared-server qualification")
				}
				init = append(init, "--server", "--external", "--server-host", "127.0.0.1", "--server-port", port, "--server-user", "root")
			}
			call(init...)
			// Graph metadata has the stricter I-JSON admission contract. Reject
			// duplicates before the ordinary Issue merge collapses object keys.
			refuse("invalid_properties", "remember", "Duplicate body", "--id", "duplicate", "--metadata", `{"x":1,"x":2}`)
			memory := graphMixedResult[graphstore.Record](t, call("remember", "First body", "--id", "plan", "--metadata", `{"team":"docs"}`))
			if !bytes.Equal(memory.Metadata, []byte(`{"team":"docs"}`)) {
				t.Fatalf("Memory metadata = %s", memory.Metadata)
			}
			createdWithSet := graphMixedResult[graphstore.Record](t, call("remember", "Second body", "--id", "note", "--set-metadata", "priority=2"))
			if !bytes.Equal(createdWithSet.Metadata, []byte(`{"priority":2}`)) {
				t.Fatalf("Memory set-on-create metadata = %s", createdWithSet.Metadata)
			}
			createOnly := graphMixedResult[graphstore.Record](t, call("remember", "Third body", "--id", "one-shot", "--create-only", "--set-metadata", "source=manual"))
			if !bytes.Equal(createOnly.Metadata, []byte(`{"source":"manual"}`)) {
				t.Fatalf("Memory create-only metadata = %s", createOnly.Metadata)
			}
			unsetByRemember := graphMixedResult[graphstore.MemoryMutationResult](t, call("remember", "--id", "note", "--unset-metadata", "priority"))
			if !unsetByRemember.Changed || !bytes.Equal(unsetByRemember.Memory.Metadata, []byte(`{}`)) {
				t.Fatalf("remember unset-only metadata = %+v", unsetByRemember)
			}
			issue := graphMixedResult[graphstore.IssueRecord](t, call("create", "Work", "--id", "work", "--metadata", `{"team":"issues"}`))
			refuse("invalid_properties", "update", issue.ID, "--metadata", `{"x":1,"x":2}`, "--if-revision", issue.Revision)
			if !bytes.Equal(issue.Metadata, []byte(`{"team":"issues"}`)) || len(issue.Properties.Metadata) != 0 {
				t.Fatalf("Issue metadata = %s, properties=%+v", issue.Metadata, issue.Properties)
			}
			link := graphMixedResult[graphstore.LinkMutationResult](t, call("link", memory.ID, issue.ID, "--id", "context", "--resource-type", scope+"types/preview-related-v2", "--metadata", `{"origin":"manual"}`, "--if-source-revision", memory.Revision))
			if !bytes.Equal(link.Link.Metadata, []byte(`{"origin":"manual"}`)) {
				t.Fatalf("Link metadata = %s", link.Link.Metadata)
			}
			owned := graphMixedResult[graphstore.Record](t, call("show", memory.ID))
			refuse("revision_conflict", "update", memory.ID, "--metadata", `{"team":"release"}`, "--if-revision", memory.Revision)
			changed := graphMixedResult[graphstore.MemoryMutationResult](t, call("update", memory.ID, "--metadata", `{"team":"release"}`, "--if-revision", owned.Revision))
			if !changed.Changed || !bytes.Equal(changed.Memory.Metadata, []byte(`{"team":"release"}`)) || changed.Memory.Properties != owned.Properties {
				t.Fatalf("metadata-only Memory update = %+v", changed)
			}
			memoryNoop := graphMixedResult[graphstore.MemoryMutationResult](t, call("update", memory.ID, "--metadata", `{"team":"release"}`, "--if-revision", changed.Memory.Revision))
			if memoryNoop.Changed || memoryNoop.Memory.Revision != changed.Memory.Revision {
				t.Fatalf("metadata no-op changed Memory = %+v", memoryNoop)
			}
			linkChanged := graphMixedResult[graphstore.LinkMutationResult](t, call("update", link.Link.ID, "--set-metadata", "origin=reviewed", "--if-revision", link.Link.Revision, "--if-source-revision", changed.Memory.Revision))
			if !linkChanged.Changed || !bytes.Equal(linkChanged.Link.Metadata, []byte(`{"origin":"reviewed"}`)) {
				t.Fatalf("metadata-only Link update = %+v", linkChanged)
			}
			refuse("revision_conflict", "update", link.Link.ID, "--unset-metadata", "origin", "--if-revision", linkChanged.Link.Revision, "--if-source-revision", changed.Memory.Revision)
			issueChanged := graphMixedResult[graphstore.IssueMutationResult](t, call("update", issue.ID, "--metadata", `{"team":"release"}`, "--if-revision", issue.Revision))
			if !issueChanged.Changed || !bytes.Equal(issueChanged.Issue.Metadata, []byte(`{"team":"release"}`)) {
				t.Fatalf("metadata-only Issue update = %+v", issueChanged)
			}
			// Existing-ID remember keeps the upsert default and uses the same
			// checked Memory writer for a metadata-only merge.
			remembered := graphMixedResult[graphstore.MemoryMutationResult](t, call("remember", "--id", "plan", "--metadata", `{"reviewed":true}`))
			if !remembered.Changed || remembered.Memory.Properties != changed.Memory.Properties || !bytes.Equal(remembered.Memory.Metadata, []byte(`{"reviewed":true,"team":"release"}`)) {
				t.Fatalf("remember metadata-only upsert = %+v", remembered)
			}
			cleared := graphMixedResult[graphstore.MemoryMutationResult](t, call("update", memory.ID, "--unset-metadata", "reviewed", "--unset-metadata", "team", "--if-revision", remembered.Memory.Revision))
			if !cleared.Changed || !bytes.Equal(cleared.Memory.Metadata, []byte(`{}`)) {
				t.Fatalf("metadata clear = %+v", cleared)
			}
			for _, saved := range []struct {
				id, version string
				metadata    []byte
			}{
				{memory.ID, memory.Revision, memory.Metadata},
				{issue.ID, issue.Revision, issue.Metadata},
				{link.Link.ID, link.Link.Revision, link.Link.Metadata},
			} {
				raw := graphMixedResult[map[string]any](t, call("show", saved.id, "--version", saved.version))
				metadata, err := json.Marshal(raw["metadata"])
				if err != nil || !bytes.Equal(metadata, saved.metadata) {
					t.Fatalf("retained %s metadata = %s, want %s (%v)", saved.id, metadata, saved.metadata, err)
				}
			}
		})
	}
}
