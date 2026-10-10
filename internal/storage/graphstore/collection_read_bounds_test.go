//go:build cgo

package graphstore

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// Real engines verify public writes roll back before publishing unreadable
// state, and reads refuse oversized state left by old or out-of-band writers.
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
						seedUnreadableMemory(t, ctx, s, "beads/large", strings.Repeat("x", PreviewCurrentReadByteLimit/2+1024))
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
						if _, err := s.CreateIssue(ctx, "beads/issue", request); !errors.Is(err, ErrLimitExceeded) {
							t.Fatalf("oversized Issue write: %v", err)
						}
						if _, err := s.CurrentSnapshot(ctx); err != nil {
							t.Fatal(err)
						}
					case "owned-link-and-history":
						source, err := s.Create(ctx, CreateRequest{Path: "beads/source"})
						if err != nil {
							t.Fatal(err)
						}
						// This note's two persisted Link copies plus the owner's retained copy
						// would fit without accounting for repeated owned/top-level acquisition.
						request := LinkCreateRequest{Path: "links/large", SourcePath: "beads/source", TargetPath: "beads/small", ExpectedSourceRevision: source.Revision, Properties: map[string]any{"note": strings.Repeat("n", PreviewCurrentReadByteLimit/5+4096)}}
						if _, err := s.AddInformationalLink(ctx, request); !errors.Is(err, ErrLimitExceeded) {
							t.Fatalf("oversized Link write: %v", err)
						}
						request.Properties = map[string]any{"note": "small current value"}
						added, err := s.AddInformationalLink(ctx, request)
						if err != nil {
							t.Fatal(err)
						}
						// Older large snapshots exceed the current budget collectively,
						// but are never charged to current-state acquisition.
						owned := added.Source.(Record)
						for _, note := range []string{strings.Repeat("h", PreviewCurrentReadByteLimit/6), strings.Repeat("j", PreviewCurrentReadByteLimit/6), "small current value"} {
							changed, err := s.UpdateLink(ctx, LinkUpdateRequest{Path: "links/large", ExpectedRevision: added.Link.Revision, ExpectedSourceRevision: owned.Revision, Properties: map[string]any{"note": note}})
							if err != nil {
								t.Fatal(err)
							}
							added.Link, owned = changed.Link, changed.Source.(Record)
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
