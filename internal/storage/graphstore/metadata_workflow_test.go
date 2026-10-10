//go:build cgo

package graphstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	publicops "github.com/steveyegge/beads/issueops"
)

func TestCommonMetadataStoredAndRetainedAcrossResourceKinds(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, o := issueExperimentOptions(t, backend)
			s, err := OpenExisting(ctx, o)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })

			memory, err := s.Create(ctx, CreateRequest{Path: "beads/plan", Title: "Plan", Body: "Body", Metadata: json.RawMessage(`{"team":"docs"}`)})
			if err != nil || !bytes.Equal(memory.Metadata, []byte(`{"team":"docs"}`)) {
				t.Fatalf("Memory create metadata = %s, %v", memory.Metadata, err)
			}
			issueRequest := plainIssue("Work")
			issueRequest.Issue.Metadata = json.RawMessage(`{"team":"issues"}`)
			issue, err := s.CreateIssue(ctx, "beads/work", issueRequest)
			if err != nil || !bytes.Equal(issue.Metadata, []byte(`{"team":"issues"}`)) || len(issue.Properties.Metadata) != 0 {
				t.Fatalf("Issue create metadata = %s, properties=%+v, %v", issue.Metadata, issue.Properties, err)
			}
			link, err := s.AddInformationalLink(ctx, LinkCreateRequest{Path: "links/context", SourcePath: "beads/plan", TargetPath: "beads/work", ExpectedSourceRevision: memory.Revision, Properties: map[string]any{"note": "original"}, Metadata: json.RawMessage(`{"source":"manual"}`)})
			if err != nil || !bytes.Equal(link.Link.Metadata, []byte(`{"source":"manual"}`)) {
				t.Fatalf("Link create metadata = %s, %v", link.Link.Metadata, err)
			}
			owned := link.Source.(Record)
			merge := publicops.MetadataPatch{Merge: publicops.Field[json.RawMessage]{Set: true, Value: json.RawMessage(`{"team":"release"}`)}}
			memoryChanged, err := s.PatchMemory(ctx, MemoryPatchRequest{Path: "beads/plan", ExpectedRevision: owned.Revision, Metadata: merge})
			if err != nil || !memoryChanged.Changed || memoryChanged.Memory.Properties != memory.Properties || !bytes.Equal(memoryChanged.Memory.Metadata, []byte(`{"team":"release"}`)) {
				t.Fatalf("Memory metadata-only edit = %+v, %v", memoryChanged, err)
			}
			if _, err := s.PatchMemory(ctx, MemoryPatchRequest{Path: "beads/plan", ExpectedRevision: owned.Revision, Metadata: merge}); !errors.Is(err, ErrConflict) {
				t.Fatalf("stale Memory metadata no-op guard = %v", err)
			}
			memoryNoop, err := s.PatchMemory(ctx, MemoryPatchRequest{Path: "beads/plan", ExpectedRevision: memoryChanged.Memory.Revision, Metadata: merge})
			if err != nil || memoryNoop.Changed || memoryNoop.Memory.Revision != memoryChanged.Memory.Revision {
				t.Fatalf("Memory metadata no-op = %+v, %v", memoryNoop, err)
			}
			linkChanged, err := s.UpdateLink(ctx, LinkUpdateRequest{Path: "links/context", ExpectedRevision: link.Link.Revision, ExpectedSourceRevision: memoryChanged.Memory.Revision, MetadataOnly: true, Metadata: publicops.MetadataPatch{Set: map[string]json.RawMessage{"source": json.RawMessage(`"reviewed"`)}}})
			if err != nil || !linkChanged.Changed || linkChanged.Link.Properties["note"] != "original" || !bytes.Equal(linkChanged.Link.Metadata, []byte(`{"source":"reviewed"}`)) {
				t.Fatalf("Link metadata-only edit = %+v, %v", linkChanged, err)
			}
			if _, err := s.UpdateLink(ctx, LinkUpdateRequest{Path: "links/context", ExpectedRevision: linkChanged.Link.Revision, ExpectedSourceRevision: memoryChanged.Memory.Revision, MetadataOnly: true, Metadata: merge}); !errors.Is(err, ErrConflict) {
				t.Fatalf("stale owning-source guard = %v", err)
			}
			issueChanged, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "editor", ExpectedRevision: issue.Revision, Metadata: merge})
			if err != nil || !issueChanged.Changed || !bytes.Equal(issueChanged.Issue.Metadata, []byte(`{"team":"release"}`)) || len(issueChanged.Issue.Properties.Metadata) != 0 {
				t.Fatalf("Issue metadata-only edit = %+v, %v", issueChanged, err)
			}
			// The ordinary metadata merge unmarshals an object into a map. Graph
			// admission must reject duplicate raw members before that loses them.
			beforeDuplicate := workflowState(t, ctx, s)
			duplicate := publicops.MetadataPatch{Merge: publicops.Field[json.RawMessage]{Set: true, Value: json.RawMessage(`{"team":"one","team":"two"}`)}}
			if got, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "editor", ExpectedRevision: issueChanged.Issue.Revision, Metadata: duplicate}); !errors.Is(err, storage.ErrValidation) || !reflect.DeepEqual(got, IssueMutationResult{}) {
				t.Fatalf("duplicate metadata merge must refuse with zero Issue result: %+v, %v", got, err)
			}
			if !reflect.DeepEqual(beforeDuplicate, workflowState(t, ctx, s)) {
				t.Fatal("duplicate metadata merge changed native Issue or History state")
			}
			// The ordinary Issue writer accepts broader JSON than graph I-JSON.
			// A refused graph update must roll back its native row and History too.
			invalid := publicops.MetadataPatch{Merge: publicops.Field[json.RawMessage]{Set: true, Value: json.RawMessage(`{"nanoseconds":1727000000000000000}`)}}
			if _, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "editor", ExpectedRevision: issueChanged.Issue.Revision, Metadata: invalid}); err == nil {
				t.Fatal("Issue accepted metadata outside graph I-JSON")
			}
			unchanged, err := s.Read(ctx, "beads/work")
			if err != nil {
				t.Fatal(err)
			}
			if got := unchanged.(IssueRecord); got.Revision != issueChanged.Issue.Revision || !bytes.Equal(got.Metadata, issueChanged.Issue.Metadata) {
				t.Fatalf("invalid metadata changed Issue: %+v", got)
			}
			for _, want := range []struct {
				path, version string
				metadata      json.RawMessage
			}{
				{"beads/plan", memory.Version, memory.Metadata},
				{"beads/plan", memoryChanged.Memory.Version, memoryChanged.Memory.Metadata},
				{"beads/work", issue.Version, issue.Metadata},
				{"beads/work", issueChanged.Issue.Version, issueChanged.Issue.Metadata},
				{"links/context", link.Link.Version, link.Link.Metadata},
				{"links/context", linkChanged.Link.Version, linkChanged.Link.Metadata},
			} {
				retained, err := s.ReadVersion(ctx, want.path, want.version)
				if err != nil {
					t.Fatalf("retained %s %s: %v", want.path, want.version, err)
				}
				var got json.RawMessage
				switch value := retained.(type) {
				case Record:
					got = value.Metadata
				case IssueRecord:
					got = value.Metadata
				case LinkRecord:
					got = value.Metadata
				}
				if !bytes.Equal(got, want.metadata) {
					t.Fatalf("retained %s %s metadata = %s, want %s", want.path, want.version, got, want.metadata)
				}
			}
		})
	}
}

func TestCommonMetadataPatchValidatesEachRawInput(t *testing.T) {
	for _, patch := range []publicops.MetadataPatch{
		{Merge: publicops.Field[json.RawMessage]{Set: true, Value: json.RawMessage(`{"x":1,"x":2}`)}},
		{Replace: publicops.Field[json.RawMessage]{Set: true, Value: json.RawMessage(`{"x":1,"x":2}`)}},
		{Set: map[string]json.RawMessage{"x": json.RawMessage(`{"a":1,"a":2}`)}, Unset: []string{"x"}},
	} {
		if err := validateCommonMetadataPatch(patch); !errors.Is(err, storage.ErrValidation) {
			t.Fatalf("raw duplicate metadata patch admitted: %+v, %v", patch, err)
		}
	}
}

// Metadata-only Issue edits must obey the same post-write acquisition budget
// as notes edits and Memory/Link metadata patches. The original Issue row,
// native History, graph mapping and sibling title all roll back together.
func TestIssueMetadataPatchPreservesCurrentReadBudget(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, o := issueExperimentOptions(t, backend)
			s, err := OpenExisting(ctx, o)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			original, err := s.CreateIssue(ctx, "beads/work", plainIssue("Original"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Create(ctx, CreateRequest{Path: "beads/context", Body: strings.Repeat("x", 7<<20)}); err != nil {
				t.Fatal(err)
			}
			before := workflowState(t, ctx, s)
			title := "Must roll back"
			metadata := json.RawMessage(`{"note":"` + strings.Repeat("m", 1536<<10) + `"}`)
			got, err := s.UpdateIssue(ctx, UpdateIssueRequest{
				Path: "beads/work", Actor: "editor", ExpectedRevision: original.Revision,
				Title: &title, Metadata: publicops.MetadataPatch{Merge: publicops.Field[json.RawMessage]{Set: true, Value: metadata}},
			})
			if !errors.Is(err, ErrLimitExceeded) || !reflect.DeepEqual(got, IssueMutationResult{}) {
				t.Fatalf("over-budget Issue metadata edit must refuse with zero result: %+v, %v", got, err)
			}
			if !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
				t.Fatal("over-budget metadata edit leaked Issue row or History")
			}
			if current, err := s.ShowIssue(ctx, "beads/work"); err != nil || !reflect.DeepEqual(current, original) {
				t.Fatalf("Issue changed after metadata refusal: %+v, %v", current, err)
			}
		})
	}
}
