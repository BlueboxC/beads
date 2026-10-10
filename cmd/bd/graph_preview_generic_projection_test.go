package main

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	graph "github.com/steveyegge/beads/graphops"
	"github.com/steveyegge/beads/internal/storage/graphstore"
	"github.com/steveyegge/beads/internal/types"
)

const genericTestScope = "https://example.test/graph/"

type genericTestEdge struct{ id, source, target string }

func genericTestSnapshot(t *testing.T, edges ...genericTestEdge) graphstore.Snapshot {
	t.Helper()
	attribution := graphstore.Attribution{Actor: "author", Status: "claimed", RecordedAt: "2026-09-28T00:00:00Z"}
	snapshot := graphstore.Snapshot{Records: []any{}}
	for _, id := range []string{"a", "b", "c", "d"} {
		snapshot.Records = append(snapshot.Records, graphstore.Record{ID: genericTestScope + "beads/" + id, Type: graphstore.MemoryTypeURL(genericTestScope), Version: "v-" + id, Revision: "v-" + id, Properties: graphstore.Properties{Title: "Title " + id + " — 雪", Body: "SECRET_BODY"}, Attribution: attribution})
	}
	for _, edge := range edges {
		snapshot.Records = append(snapshot.Records, graphstore.LinkRecord{ID: genericTestScope + "links/" + edge.id, Type: graphstore.RelatedTypeURL(genericTestScope), Source: genericTestScope + "beads/" + edge.source, Target: genericTestScope + "beads/" + edge.target, Version: "v-" + edge.id, Revision: "v-" + edge.id, Properties: map[string]any{"note": "SECRET_NOTE"}, Attribution: attribution})
	}
	return snapshot
}

func genericTestInput(root, direction string, depth int) graphGenericInput {
	return graphGenericInput{Root: genericTestScope + "beads/" + root, Direction: direction, Depth: depth, MaxNodes: 100, MaxLinks: 200}
}
func assertGenericSelection(t *testing.T, got graphGenericResult, nodes, links, frontier []string) {
	t.Helper()
	nodeIDs, linkIDs, frontierIDs := []string{}, []string{}, []string{}
	for _, node := range got.Nodes {
		nodeIDs = append(nodeIDs, strings.TrimPrefix(node.ID, genericTestScope+"beads/"))
	}
	for _, link := range got.Links {
		linkIDs = append(linkIDs, strings.TrimPrefix(link.ID, genericTestScope+"links/"))
	}
	for _, id := range got.Frontier {
		frontierIDs = append(frontierIDs, strings.TrimPrefix(id, genericTestScope+"beads/"))
	}
	if !reflect.DeepEqual(nodeIDs, nodes) || !reflect.DeepEqual(linkIDs, links) || !reflect.DeepEqual(frontierIDs, frontier) || got.Complete != (len(frontier) == 0) {
		t.Fatalf("selection nodes=%v links=%v frontier=%v complete=%t", nodeIDs, linkIDs, frontierIDs, got.Complete)
	}
	if got.Nodes == nil || got.Links == nil || got.Frontier == nil {
		t.Fatal("nil output array")
	}
}

func TestGraphPreviewGenericDirectionsAndDepth(t *testing.T) {
	snapshot := genericTestSnapshot(t, genericTestEdge{"ab", "a", "b"}, genericTestEdge{"bc", "b", "c"})
	for _, tc := range []struct {
		name, root, direction  string
		depth                  int
		nodes, links, frontier []string
	}{
		{"isolated", "d", "both", 0, []string{"d"}, []string{}, []string{}},
		{"out-zero", "a", "out", 0, []string{"a"}, []string{}, []string{"a"}},
		{"out-one", "a", "out", 1, []string{"a", "b"}, []string{"ab"}, []string{"b"}},
		{"out-two", "a", "out", 2, []string{"a", "b", "c"}, []string{"ab", "bc"}, []string{}},
		{"in-one", "b", "in", 1, []string{"a", "b"}, []string{"ab"}, []string{}},
		{"out-other", "b", "out", 1, []string{"b", "c"}, []string{"bc"}, []string{}},
		{"both", "b", "both", 1, []string{"a", "b", "c"}, []string{"ab", "bc"}, []string{}},
		{"reverse-chain", "c", "in", 2, []string{"a", "b", "c"}, []string{"ab", "bc"}, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := genericTestInput(tc.root, tc.direction, tc.depth)
			got, err := projectGraphGeneric(snapshot, genericTestScope, in)
			if err != nil {
				t.Fatal(err)
			}
			assertGenericSelection(t, got, tc.nodes, tc.links, tc.frontier)
			if got.Projection != "summary" || got.Scope != genericTestScope || got.Root != in.Root || got.Direction != in.Direction || got.Depth != in.Depth || got.MaxNodes != in.MaxNodes || got.MaxLinks != in.MaxLinks {
				t.Fatal("request context missing from summary")
			}
		})
	}
}

func TestGraphPreviewGenericLinkIdentityFrontier(t *testing.T) {
	// Prior art: #5283's repeated-node/diamond regression intent and #6148's
	// direction/permutation concern; neither contributor algorithm is reused.
	for _, tc := range []struct {
		name, direction        string
		depth                  int
		edges                  []genericTestEdge
		nodes, links, frontier []string
	}{
		{"parallel-self", "out", 1, []genericTestEdge{{"ab1", "a", "b"}, {"ab2", "a", "b"}, {"aa", "a", "a"}}, []string{"a", "b"}, []string{"aa", "ab1", "ab2"}, []string{}},
		{"root-self-zero", "both", 0, []genericTestEdge{{"aa", "a", "a"}}, []string{"a"}, []string{}, []string{"a"}},
		{"boundary-self", "out", 1, []genericTestEdge{{"ab", "a", "b"}, {"bb", "b", "b"}}, []string{"a", "b"}, []string{"ab"}, []string{"b"}},
		{"boundary-self-expanded", "out", 2, []genericTestEdge{{"ab", "a", "b"}, {"bb", "b", "b"}}, []string{"a", "b"}, []string{"ab", "bb"}, []string{}},
		{"cycle-boundary", "out", 1, []genericTestEdge{{"ab", "a", "b"}, {"ba", "b", "a"}}, []string{"a", "b"}, []string{"ab"}, []string{"b"}},
		{"cycle-closed", "out", 2, []genericTestEdge{{"ab", "a", "b"}, {"ba", "b", "a"}}, []string{"a", "b"}, []string{"ab", "ba"}, []string{}},
		{"cycle-both", "both", 1, []genericTestEdge{{"ab", "a", "b"}, {"ba", "b", "a"}}, []string{"a", "b"}, []string{"ab", "ba"}, []string{}},
		{"diamond-out", "out", 1, []genericTestEdge{{"ab", "a", "b"}, {"ac", "a", "c"}, {"bc", "b", "c"}}, []string{"a", "b", "c"}, []string{"ab", "ac"}, []string{"b"}},
		{"diamond-both", "both", 1, []genericTestEdge{{"ab", "a", "b"}, {"ac", "a", "c"}, {"bc", "b", "c"}}, []string{"a", "b", "c"}, []string{"ab", "ac"}, []string{"b", "c"}},
		{"diamond-complete", "out", 2, []genericTestEdge{{"ab", "a", "b"}, {"ac", "a", "c"}, {"bc", "b", "c"}}, []string{"a", "b", "c"}, []string{"ab", "ac", "bc"}, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := genericTestSnapshot(t, tc.edges...)
			got, err := projectGraphGeneric(snapshot, genericTestScope, genericTestInput("a", tc.direction, tc.depth))
			if err != nil {
				t.Fatal(err)
			}
			assertGenericSelection(t, got, tc.nodes, tc.links, tc.frontier)
			// Reversing the full input moves Links before nodes as well as reversing
			// adjacency insertion order; summary bytes must remain identical.
			reversed := graphstore.Snapshot{Records: append([]any(nil), snapshot.Records...)}
			for i, j := 0, len(reversed.Records)-1; i < j; i, j = i+1, j-1 {
				reversed.Records[i], reversed.Records[j] = reversed.Records[j], reversed.Records[i]
			}
			again, err := projectGraphGeneric(reversed, genericTestScope, genericTestInput("a", tc.direction, tc.depth))
			if err != nil || !reflect.DeepEqual(got, again) {
				t.Fatalf("permutation changed traversal: %v", err)
			}
		})
	}
}

func TestGraphPreviewGenericBoundsAndInvalidSnapshot(t *testing.T) {
	snapshot := genericTestSnapshot(t, genericTestEdge{"ab", "a", "b"}, genericTestEdge{"ab2", "a", "b"}, genericTestEdge{"bc", "b", "c"})
	for _, tc := range []struct {
		name   string
		mutate func(*graphGenericInput)
	}{
		{"node-exhaustion", func(in *graphGenericInput) { in.MaxNodes = 1 }},
		{"link-exhaustion", func(in *graphGenericInput) { in.MaxLinks = 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := genericTestInput("a", "out", 2)
			tc.mutate(&in)
			got, err := projectGraphGeneric(snapshot, genericTestScope, in)
			if !errors.Is(err, graphstore.ErrLimitExceeded) || !reflect.DeepEqual(got, graphGenericResult{}) {
				t.Fatalf("cap leaked partial result: %+v %v", got, err)
			}
		})
	}
	t.Run("internal-invalid-bounds", func(t *testing.T) {
		in := genericTestInput("a", "out", -1)
		got, err := projectGraphGeneric(snapshot, genericTestScope, in)
		if !errors.Is(err, graph.ErrValidation) || !reflect.DeepEqual(got, graphGenericResult{}) {
			t.Fatalf("invalid internal request misclassified: %+v %v", got, err)
		}
	})
	t.Run("frontier-does-not-consume-cap", func(t *testing.T) {
		in := genericTestInput("a", "out", 0)
		in.MaxNodes = 1
		in.MaxLinks = 1
		got, err := projectGraphGeneric(snapshot, genericTestScope, in)
		if err != nil {
			t.Fatal(err)
		}
		assertGenericSelection(t, got, []string{"a"}, []string{}, []string{"a"})
	})
	t.Run("exact-node-and-link-caps", func(t *testing.T) {
		in := genericTestInput("a", "out", 1)
		in.MaxNodes = 2
		in.MaxLinks = 2
		got, err := projectGraphGeneric(snapshot, genericTestScope, in)
		if err != nil {
			t.Fatal(err)
		}
		assertGenericSelection(t, got, []string{"a", "b"}, []string{"ab", "ab2"}, []string{"b"})
	})
	t.Run("self-one-node-one-link", func(t *testing.T) {
		in := genericTestInput("a", "both", 1)
		in.MaxNodes = 1
		in.MaxLinks = 1
		got, err := projectGraphGeneric(genericTestSnapshot(t, genericTestEdge{"aa", "a", "a"}), genericTestScope, in)
		if err != nil {
			t.Fatal(err)
		}
		assertGenericSelection(t, got, []string{"a"}, []string{"aa"}, []string{})
	})
	t.Run("missing-root", func(t *testing.T) {
		got, err := projectGraphGeneric(snapshot, genericTestScope, genericTestInput("missing", "out", 1))
		if !errors.Is(err, graphstore.ErrNotFound) || !reflect.DeepEqual(got, graphGenericResult{}) {
			t.Fatalf("missing root: %+v %v", got, err)
		}
	})
	for _, tc := range []struct {
		name   string
		mutate func(*graphstore.Snapshot)
	}{
		{"unknown-kind", func(s *graphstore.Snapshot) { s.Records = append(s.Records, "unknown") }},
		{"duplicate-bead", func(s *graphstore.Snapshot) { s.Records = append(s.Records, s.Records[0]) }},
		{"duplicate-link", func(s *graphstore.Snapshot) { s.Records = append(s.Records, s.Records[4]) }},
		{"missing-endpoint", func(s *graphstore.Snapshot) {
			r := s.Records[4].(graphstore.LinkRecord)
			r.Target = genericTestScope + "beads/missing"
			s.Records[4] = r
		}},
		{"foreign-endpoint", func(s *graphstore.Snapshot) {
			r := s.Records[4].(graphstore.LinkRecord)
			r.Target = "https://remote.test/beads/a"
			s.Records[4] = r
		}},
		{"unsupported-type", func(s *graphstore.Snapshot) {
			r := s.Records[0].(graphstore.Record)
			r.Type = genericTestScope + "types/unknown"
			s.Records[0] = r
		}},
		{"empty-version", func(s *graphstore.Snapshot) { r := s.Records[0].(graphstore.Record); r.Version = ""; s.Records[0] = r }},
		{"noncanonical-id", func(s *graphstore.Snapshot) {
			r := s.Records[0].(graphstore.Record)
			r.ID = genericTestScope + "beads/a?version=old"
			s.Records[0] = r
		}},
		{"incomplete-issue", func(s *graphstore.Snapshot) {
			s.Records[0] = graphstore.IssueRecord{ID: genericTestScope + "beads/a", Type: graphstore.IssueTypeURL(genericTestScope), Version: "v"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			modified := graphstore.Snapshot{Records: append([]any(nil), snapshot.Records...)}
			tc.mutate(&modified)
			got, err := projectGraphGeneric(modified, genericTestScope, genericTestInput("a", "out", 1))
			if !errors.Is(err, graphstore.ErrInvalidStore) || !reflect.DeepEqual(got, graphGenericResult{}) {
				t.Fatalf("invalid snapshot accepted/partially emitted: %+v %v", got, err)
			}
		})
	}
}

func TestGraphPreviewGenericSummaryAllowlist(t *testing.T) {
	snapshot := genericTestSnapshot(t, genericTestEdge{"ab", "a", "b"})
	issue := graphstore.IssueRecord{ID: genericTestScope + "beads/a", Type: graphstore.IssueTypeURL(genericTestScope), Version: "issue-version", Revision: "issue-version", Properties: &types.Issue{ID: "SECRET_NATIVE_ID", Title: "Issue title", Description: "SECRET_DESCRIPTION", Notes: "SECRET_NOTES", Design: "SECRET_DESIGN", AcceptanceCriteria: "SECRET_ACCEPTANCE"}, Attribution: graphstore.Attribution{Status: "unknown"}}
	ownedOnly := snapshot.Records[4].(graphstore.LinkRecord)
	ownedOnly.ID = genericTestScope + "links/owned-only"
	ownedOnly.Target = genericTestScope + "beads/d"
	rawLink, err := json.Marshal(ownedOnly)
	if err != nil {
		t.Fatal(err)
	}
	rawDuplicate, err := json.Marshal(snapshot.Records[4])
	if err != nil {
		t.Fatal(err)
	}
	issue.Owned = []json.RawMessage{rawDuplicate, rawLink}
	snapshot.Records[0] = issue
	before, _ := json.Marshal(snapshot)
	got, err := projectGraphGeneric(snapshot, genericTestScope, genericTestInput("a", "out", 1))
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(snapshot)
	if string(before) != string(after) {
		t.Fatal("projection mutated supplied snapshot")
	}
	output, err := renderGraphGeneric(got, true, false)
	if err != nil {
		t.Fatal(err)
	}
	assertGenericSelection(t, got, []string{"a", "b"}, []string{"ab"}, []string{})
	if strings.Contains(output, "SECRET_") || strings.Contains(output, "owned-only") || len(got.Links) != 1 {
		t.Fatal("bulk content leaked or Owned Link copy duplicated")
	}
	var envelope struct{ Result map[string]json.RawMessage }
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatal(err)
	}
	var nodes, links []map[string]any
	if err := json.Unmarshal(envelope.Result["nodes"], &nodes); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(envelope.Result["links"], &links); err != nil {
		t.Fatal(err)
	}
	for _, node := range nodes {
		wantFields := 5
		if node["id"] == issue.ID {
			wantFields = 4 // No principal was recorded for the native Issue.
			if _, present := node["attribution"]; present {
				t.Fatalf("principal-free attribution leaked: %+v", node)
			}
		} else if node["attribution"] == nil {
			t.Fatalf("attributed Memory lost its attribution: %+v", node)
		}
		if len(node) != wantFields || node["id"] == nil || node["type"] == nil || node["title"] == nil || node["version"] == nil {
			t.Fatalf("node field allowlist differs: %+v", node)
		}
	}
	for _, link := range links {
		if len(link) != 6 || link["id"] == nil || link["type"] == nil || link["source"] == nil || link["target"] == nil || link["version"] == nil || link["attribution"] == nil {
			t.Fatalf("Link field allowlist differs: %+v", link)
		}
	}
	if got.Nodes[0].Version != issue.Version || got.Nodes[0].Attribution != issue.Attribution || got.Nodes[0].Title != issue.Properties.Title {
		t.Fatal("projection invented record values")
	}
}

func TestGraphPreviewGenericCanonicalSpelling(t *testing.T) {
	snapshot := genericTestSnapshot(t, genericTestEdge{"ab", "a", "b"})
	a := snapshot.Records[0].(graphstore.Record)
	a.ID = genericTestScope + "beads/a%20b"
	a.Properties.Title = ""
	b := snapshot.Records[1].(graphstore.Record)
	b.ID = genericTestScope + "beads/%E9%9B%AA"
	b.Properties.Title = "  雪  "
	link := snapshot.Records[4].(graphstore.LinkRecord)
	link.ID = genericTestScope + "links/%E2%86%92"
	link.Source = a.ID
	link.Target = b.ID
	snapshot.Records = []any{link, a, b}
	in := genericTestInput("a", "both", 1)
	in.Root = a.ID
	got, err := projectGraphGeneric(snapshot, genericTestScope, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Nodes) != 2 || got.Nodes[0].ID != b.ID || got.Nodes[1].ID != a.ID || got.Nodes[0].Title != b.Properties.Title || got.Nodes[1].Title != "" || got.Links[0].ID != link.ID || !got.Complete {
		t.Fatalf("canonical spelling/title preservation differs: %+v", got)
	}
}
