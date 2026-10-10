//go:build cgo

package graphstore

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// Real engines, normal schema initialization and only public graph authoring
// APIs. The read budget is an operational refusal: authoring larger content is
// still admitted, and both inventory and exact reads fail before decoding it.
func TestCurrentReadAcquisitionBudget(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			for _, scenario := range []string{"memory", "issue-hydration", "owned-link-and-history"} {
				t.Run(scenario, func(t *testing.T) {
					ctx, o := issueExperimentOptions(t, backend)
					s, err := OpenExisting(ctx, o)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() {
						if err := s.Close(); err != nil {
							t.Error(err)
						}
					})
					if _, err := s.Create(ctx, CreateRequest{Path: "beads/small", Body: "small"}); err != nil {
						t.Fatal(err)
					}
					if snapshot, err := s.CurrentSnapshot(ctx); err != nil || len(snapshot.Records) != 1 {
						t.Fatalf("ordinary inventory: %v %+v", err, snapshot)
					}
					switch scenario {
					case "memory":
						if _, err := s.Create(ctx, CreateRequest{Path: "beads/large", Body: strings.Repeat("x", PreviewCurrentReadByteLimit/2+1024)}); err != nil {
							t.Fatal(err)
						}
						assertCurrentReadBudgetRefusal(t, ctx, s, "beads/large")
					case "issue-hydration":
						// The filler alone fits. A legal TEXT-sized Issue description plus its
						// current durable_state pushes acquisition over the remaining headroom.
						if _, err := s.Create(ctx, CreateRequest{Path: "beads/filler", Body: strings.Repeat("x", PreviewCurrentReadByteLimit/2-(32<<10))}); err != nil {
							t.Fatal(err)
						}
						if _, err := s.CurrentSnapshot(ctx); err != nil {
							t.Fatalf("filler alone should fit: %v", err)
						}
						request := plainIssue("bounded issue")
						request.Issue.Description = strings.Repeat("d", 60<<10)
						if _, err := s.CreateIssue(ctx, "beads/issue", request); err != nil {
							t.Fatal(err)
						}
						assertCurrentReadBudgetRefusal(t, ctx, s, "beads/issue")
					case "owned-link-and-history":
						source, err := s.Create(ctx, CreateRequest{Path: "beads/source"})
						if err != nil {
							t.Fatal(err)
						}
						// This note's two persisted Link copies plus the owner's retained copy
						// would fit without accounting for repeated owned/top-level acquisition.
						added, err := s.AddInformationalLink(ctx, LinkCreateRequest{Path: "links/large", SourcePath: "beads/source", TargetPath: "beads/small", ExpectedSourceRevision: source.Revision, Properties: map[string]any{"note": strings.Repeat("n", PreviewCurrentReadByteLimit/5+4096)}})
						if err != nil {
							t.Fatal(err)
						}
						assertCurrentReadBudgetRefusal(t, ctx, s, "links/large")
						owned := added.Source.(Record)
						// Make retained historical bytes exceed the budget while leaving only a
						// tiny current state. Neither old snapshot belongs in a current read.
						enlarged, err := s.UpdateLink(ctx, LinkUpdateRequest{Path: "links/large", ExpectedRevision: added.Link.Revision, ExpectedSourceRevision: owned.Revision, Properties: map[string]any{"note": strings.Repeat("h", PreviewCurrentReadByteLimit/2+4096)}})
						if err != nil {
							t.Fatal(err)
						}
						owned = enlarged.Source.(Record)
						if _, err := s.UpdateLink(ctx, LinkUpdateRequest{Path: "links/large", ExpectedRevision: enlarged.Link.Revision, ExpectedSourceRevision: owned.Revision, Properties: map[string]any{"note": "small current value"}}); err != nil {
							t.Fatal(err)
						}
						snapshot, err := s.CurrentSnapshot(ctx)
						if err != nil || len(snapshot.Records) != 3 {
							t.Fatalf("old history wrongly charged: %v count=%d", err, len(snapshot.Records))
						}
						if _, err := s.Read(ctx, "links/large"); err != nil {
							t.Fatal(err)
						}
					}
				})
			}
		})
	}
}

func assertCurrentReadBudgetRefusal(t *testing.T, ctx context.Context, s *Store, path string) {
	t.Helper()
	snapshot, err := s.CurrentSnapshot(ctx)
	if !errors.Is(err, ErrLimitExceeded) || !reflect.DeepEqual(snapshot, Snapshot{}) {
		t.Fatalf("inventory must refuse with zero result: %v; records=%d", err, len(snapshot.Records))
	}
	for _, path := range []string{path, "beads/small"} {
		value, err := s.Read(ctx, path)
		if !errors.Is(err, ErrLimitExceeded) || value != nil {
			t.Fatalf("exact read %s must enforce whole-workspace budget: %v; nonnil=%t", path, err, value != nil)
		}
	}
}
