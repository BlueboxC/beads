//go:build cgo

package graphread

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	graph "github.com/steveyegge/beads/graphops"
	"github.com/steveyegge/beads/internal/httpapi/bdpwire"
	"github.com/steveyegge/beads/internal/storage/graphstore"
	"github.com/steveyegge/beads/issueops"
)

// This is the real storage-to-wire boundary. Writers use normal initialization
// and public store operations; no test schema or SQL payload is seeded. It is
// not an installed CLI or HTTP interoperability demonstration.
func TestAuthoritativeRecordsProjectToPublicWire(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			// 180s, not 90s: in the -race CI lane (-p 4 -parallel 4) this subtest
			// measured 82s on a passing run and hit a 90s deadline on two
			// consecutive runs, while it takes about 40s on a fast local machine.
			// The deadline exists to catch hangs, not to measure speed.
			ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
			defer cancel()
			workspace, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			o := graphstore.Options{Backend: backend, Database: fmt.Sprintf("graph_read_%d", time.Now().UnixNano()), DataDir: filepath.Join(workspace, "dolt"), Branch: "main", Binding: graphstore.Binding{WorkspaceID: workspace, ScopeURL: "https://example.test/read/", AuthorityID: "0123456789abcdef0123456789abcdef", SchemaVersion: graphstore.SchemaVersion}}
			if backend == "server" {
				if os.Getenv("BEADS_GRAPH_TEST_SERVER_PORT") == "" {
					t.Skip("ordinary shared-server Dolt port not configured")
				}
				o.ServerPort, err = strconv.Atoi(os.Getenv("BEADS_GRAPH_TEST_SERVER_PORT"))
				if err != nil {
					t.Fatal(err)
				}
				o.ServerHost, o.ServerUser = "127.0.0.1", "root"
			}
			if err := graphstore.Init(ctx, o); err != nil {
				t.Fatal(err)
			}
			s, err := graphstore.OpenExisting(ctx, o)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := s.Close(); err != nil {
					t.Error(err)
				}
			})
			r := New(s)
			empty, err := r.Inventory(ctx)
			if err != nil || len(empty.Beads) != 0 || len(empty.Links) != 0 || len(empty.Types) != 6 || empty.WriterToken == "" {
				t.Fatalf("empty installed inventory: %+v, %v", empty, err)
			}
			memory, err := s.Create(ctx, graphstore.CreateRequest{Path: "beads/plan", Title: "Memory — 記憶", Body: "", Actor: "human:Donna <unchanged>", Metadata: json.RawMessage(`{"reviewed":true}`)})
			if err != nil {
				t.Fatal(err)
			}
			other, err := s.Create(ctx, graphstore.CreateRequest{Path: "beads/other", Title: "Target"})
			if err != nil {
				t.Fatal(err)
			}
			createIssue := func(path string) graphstore.IssueRecord {
				v, err := s.CreateIssue(ctx, path, issueops.CreateRequest{Issue: &issueops.Issue{Title: path, Status: "open", IssueType: "task", Priority: 2, Metadata: json.RawMessage(`{"team":"triage"}`)}, Actor: "agent:test"})
				if err != nil {
					t.Fatal(err)
				}
				return v
			}
			issue := createIssue("beads/task")
			_ = createIssue("beads/blocker")
			readBead := func(path string) bdpwire.BeadRecord {
				v, err := r.Resource(ctx, path)
				if err != nil {
					t.Fatal(err)
				}
				bead, ok := v.(bdpwire.BeadRecord)
				if !ok {
					t.Fatalf("not a Bead: %T", v)
				}
				checkWire(t, "beadRecord", bead)
				return bead
			}
			first := readBead("beads/plan")
			if first.ID != memory.ID || first.Revision != memory.Revision || first.OwnedLinks == nil || len(first.OwnedLinks) != 0 || string(first.Properties["body"]) != `""` || first.Attribution.Principal != memory.Attribution.Actor {
				t.Fatalf("Memory projection changed identity/content: %+v", first)
			}
			if string(first.Metadata["reviewed"]) != "true" || len(readBead("beads/other").Metadata) != 0 {
				t.Fatalf("Memory metadata projection = %#v", first.Metadata)
			}
			if readBead("beads/other").Attribution != nil {
				t.Fatal("fabricated actor")
			}
			firstIssue := readBead("beads/task")
			if string(firstIssue.Metadata["team"]) != `"triage"` {
				t.Fatalf("Issue metadata projection = %#v", firstIssue.Metadata)
			}
			if group, ok := firstIssue.OwnedLinks[graphstore.DependencyTypeURL(o.Binding.ScopeURL)]; !ok || len(group) != 0 {
				t.Fatal("missing declared empty owned group")
			}
			wantProperties, _ := json.Marshal(issue.Properties)
			gotProperties, _ := json.Marshal(firstIssue.Properties)
			var a, b any
			_ = json.Unmarshal(wantProperties, &a)
			_ = json.Unmarshal(gotProperties, &b)
			if !reflect.DeepEqual(a, b) {
				t.Fatal("Issue properties were filtered or changed")
			}
			if _, err := s.AddDependency(ctx, graphstore.DependencyRequest{SourcePath: "beads/task", TargetPath: "beads/blocker", Path: "links/blocks", Actor: "agent:test"}); err != nil {
				t.Fatal(err)
			}
			var created graphstore.LinkRecord
			for _, path := range []string{"links/z", "links/a"} {
				v, err := s.AddInformationalLink(ctx, graphstore.LinkCreateRequest{Path: path, SourcePath: "beads/plan", TargetPath: "beads/other", Actor: "human:Donna", UnconditionalSource: true, Properties: map[string]any{"note": path}, Metadata: json.RawMessage(`{"origin":"manual"}`)})
				if err != nil {
					t.Fatal(err)
				}
				created = v.Link
			}
			for _, path := range []string{"links/blocks", "links/a"} {
				v, err := r.Resource(ctx, path)
				if err != nil {
					t.Fatal(err)
				}
				checkWire(t, "linkRecord", v)
			}
			linked := readBead("beads/plan")
			inventory := checkInventory(t, ctx, r, 4, 3)
			if inventory.WriterToken == empty.WriterToken {
				t.Fatal("inventory token did not change after writes")
			}
			group := linked.OwnedLinks[created.Type]
			if len(group) != 2 || !strings.HasSuffix(group[0].ID, "/links/a") || group[1].Source.URI != memory.ID || linked.Revision == first.Revision {
				t.Fatal("owned projection ordering or membership differs")
			}
			if string(group[0].Metadata["origin"]) != `"manual"` {
				t.Fatalf("owned Link metadata projection = %#v", group[0].Metadata)
			}
			if readBead("beads/other").Revision != other.Revision {
				t.Fatal("incoming Link changed target")
			}
			withBlock := readBead("beads/task")
			if len(withBlock.OwnedLinks[graphstore.DependencyTypeURL(o.Binding.ScopeURL)]) != 1 || withBlock.Revision == firstIssue.Revision {
				t.Fatal("Issue owned state did not advance")
			}
			for _, typ := range []string{memory.Type, issue.Type, created.Type, graphstore.DependencyTypeURL(o.Binding.ScopeURL), graphstore.ExampleFollowsTypeURL(o.Binding.ScopeURL), graphstore.ExampleCitesTypeURL(o.Binding.ScopeURL)} {
				d, err := r.Type(ctx, strings.TrimPrefix(typ, o.Binding.ScopeURL))
				if err != nil {
					t.Fatal(err)
				}
				if d.ID != typ {
					t.Fatal("descriptor identity changed")
				}
				checkWire(t, "typeDescriptor", d)
			}
			if _, err := s.UpdateLink(ctx, graphstore.LinkUpdateRequest{Path: "links/a", Actor: "human:Donna", Unconditional: true, UnconditionalSource: true, Properties: map[string]any{"note": "revised"}}); err != nil {
				t.Fatal(err)
			}
			updated := readBead("beads/plan")
			updatedInventory := checkInventory(t, ctx, r, 4, 3)
			if updatedInventory.WriterToken == inventory.WriterToken {
				t.Fatal("Link property update did not invalidate inventory token")
			}
			if updated.Revision == linked.Revision || string(updated.OwnedLinks[created.Type][0].Properties["note"]) != `"revised"` {
				t.Fatal("current owned update missing")
			}
			if _, err := s.Unlink(ctx, graphstore.LinkDeleteRequest{Path: "links/a", Actor: "human:Donna", Unconditional: true, UnconditionalSource: true}); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Resource(ctx, "links/a"); !errors.Is(err, graphstore.ErrGone) {
				t.Fatalf("deleted state lost: %v", err)
			}
			if _, err := r.Resource(ctx, "links/missing"); !errors.Is(err, graphstore.ErrNotFound) {
				t.Fatalf("absence changed: %v", err)
			}
			remaining := readBead("beads/plan")
			remainingInventory := checkInventory(t, ctx, r, 4, 2)
			if remainingInventory.WriterToken == updatedInventory.WriterToken {
				t.Fatal("unlink did not invalidate inventory token")
			}
			if len(remaining.OwnedLinks[created.Type]) != 1 || remaining.Revision == updated.Revision {
				t.Fatal("deleted Link retained in current owned projection")
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = graphstore.OpenExisting(ctx, o)
			if err != nil {
				t.Fatal(err)
			}
			r = New(s)
			if !reflect.DeepEqual(remaining, readBead("beads/plan")) {
				t.Fatal("projection changed after reopen")
			}
			if !reflect.DeepEqual(remainingInventory, checkInventory(t, ctx, r, 4, 2)) {
				t.Fatal("inventory changed after reopen without a write")
			}
			checkStoredSelectionAndPages(t, ctx, r)
		})
	}
}

func checkInventory(t *testing.T, ctx context.Context, reader *Reader, beads, links int) Inventory {
	t.Helper()
	value, err := reader.Inventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(value.Beads) != beads || len(value.Links) != links || len(value.Types) != 6 {
		t.Fatalf("incomplete inventory: %d Beads, %d Links, %d Types", len(value.Beads), len(value.Links), len(value.Types))
	}
	verify := func(id string, record any) {
		t.Helper()
		one, err := reader.Resource(ctx, strings.TrimPrefix(id, reader.store.ScopeURL()))
		if err != nil || !reflect.DeepEqual(one, record) {
			t.Fatalf("inventory differs from exact read of %s: %v", id, err)
		}
	}
	for i, record := range value.Beads {
		if i > 0 && graph.CompareCodeUnits(value.Beads[i-1].ID, record.ID) >= 0 {
			t.Fatal("Bead inventory order")
		}
		verify(record.ID, record)
	}
	for i, record := range value.Links {
		if i > 0 && graph.CompareCodeUnits(value.Links[i-1].ID, record.ID) >= 0 {
			t.Fatal("Link inventory order")
		}
		verify(record.ID, record)
	}
	for i, record := range value.Types {
		if i > 0 && graph.CompareCodeUnits(value.Types[i-1].ID, record.ID) >= 0 {
			t.Fatal("Type inventory order")
		}
		one, err := reader.Type(ctx, strings.TrimPrefix(record.ID, reader.store.ScopeURL()))
		if err != nil || !reflect.DeepEqual(one, record) {
			t.Fatalf("inventory differs from exact Type read: %v", err)
		}
	}
	return value
}

func checkWire(t *testing.T, kind string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	switch kind {
	case "beadRecord":
		decoded = &bdpwire.BeadRecord{}
	case "linkRecord":
		decoded = &bdpwire.LinkRecord{}
	case "typeDescriptor":
		decoded = &bdpwire.TypeDescriptor{}
	}
	if err := bdpwire.Unmarshal(raw, decoded); err != nil {
		t.Fatal(err)
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"version", "owned", "actor", "recordedAt"} {
		if _, ok := members[name]; ok {
			t.Fatalf("private member leaked: %s", name)
		}
	}
	receipt, _ := json.Marshal(map[string]any{"kind": kind, "value": value})
	t.Logf("WIRE_RECEIPT %s", receipt)
}
