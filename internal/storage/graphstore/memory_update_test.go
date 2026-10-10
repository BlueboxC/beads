//go:build cgo

package graphstore

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

func TestMemoryUpdateLifecycle(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, o := issueExperimentOptions(t, backend)
			s, err := OpenExisting(ctx, o)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := s.Close(); err != nil {
					t.Error(err)
				}
			}()
			original, err := s.Create(ctx, CreateRequest{Path: "beads/plan", Title: "Plan", Body: "Original — 雪\n", Actor: "author"})
			if err != nil {
				t.Fatal(err)
			}
			issue, err := s.CreateIssue(ctx, "beads/work", plainIssue("Work"))
			if err != nil {
				t.Fatal(err)
			}
			incoming, err := s.Create(ctx, CreateRequest{Path: "beads/incoming", Body: "Cites plan"})
			if err != nil {
				t.Fatal(err)
			}
			in, err := s.AddInformationalLink(ctx, LinkCreateRequest{Path: "links/in", SourcePath: "beads/incoming", TargetPath: "beads/plan", ExpectedSourceRevision: incoming.Revision})
			if err != nil {
				t.Fatal(err)
			}
			added, err := s.AddInformationalLink(ctx, LinkCreateRequest{Path: "links/out", SourcePath: "beads/plan", TargetPath: "beads/work", ExpectedSourceRevision: original.Revision, Properties: map[string]any{"note": "before"}})
			if err != nil {
				t.Fatal(err)
			}
			owned := added.Source.(Record)
			request := MemoryUpdateRequest{Path: "beads/plan", Properties: Properties{Title: "Revised 記憶", Body: "  # Revised\n\n"}, Actor: "editor", ExpectedRevision: owned.Revision}
			updated, err := s.UpdateMemory(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			if !updated.Changed || updated.Memory.Revision == owned.Revision || updated.Memory.Version != updated.Memory.Revision || updated.Memory.ID != owned.ID || updated.Memory.Type != owned.Type || updated.Memory.Properties != request.Properties || updated.Memory.Attribution.Actor != "editor" || updated.Memory.Attribution.Status != "claimed" || !reflect.DeepEqual(updated.Memory.Owned, owned.Owned) {
				t.Fatalf("wrong replacement: %+v", updated)
			}
			assertOwnedLink(t, updated.Memory, added.Link)
			if got, err := s.ShowLink(ctx, "links/out"); err != nil || !reflect.DeepEqual(got, added.Link) {
				t.Fatalf("replacement changed outgoing Link: %+v %v", got, err)
			}
			if got, err := s.ShowLink(ctx, "links/in"); err != nil || !reflect.DeepEqual(got, in.Link) {
				t.Fatalf("replacement changed incoming Link: %+v %v", got, err)
			}
			if got, err := s.Show(ctx, "beads/incoming"); err != nil || !reflect.DeepEqual(got, in.Source) {
				t.Fatalf("replacement changed incoming owner: %+v %v", got, err)
			}
			if got, err := s.ShowIssue(ctx, "beads/work"); err != nil || !reflect.DeepEqual(got, issue) {
				t.Fatalf("replacement changed target: %+v %v", got, err)
			}
			before := workflowState(t, ctx, s)
			if _, err := s.UpdateMemory(ctx, request); !errors.Is(err, ErrConflict) {
				t.Fatalf("stale no-op: %v", err)
			}
			request.ExpectedRevision = updated.Memory.Revision
			request.Actor = "different-actor"
			noop, err := s.UpdateMemory(ctx, request)
			if err != nil || noop.Changed || !reflect.DeepEqual(noop.Memory, updated.Memory) {
				t.Fatalf("no-op: %+v %v", noop, err)
			}
			if !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
				t.Fatal("no-op or stale guard changed stored state")
			}
			changedLink, err := s.UpdateLink(ctx, LinkUpdateRequest{Path: "links/out", Properties: map[string]any{"note": "after"}, ExpectedRevision: added.Link.Revision, ExpectedSourceRevision: updated.Memory.Revision})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.UpdateMemory(ctx, request); !errors.Is(err, ErrConflict) {
				t.Fatalf("owned-Link change did not invalidate content guard: %v", err)
			}
			cleared, err := s.UpdateMemory(ctx, MemoryUpdateRequest{Path: "beads/plan", Properties: Properties{}, Unconditional: true})
			if err != nil || !cleared.Changed || cleared.Memory.Properties != (Properties{}) || cleared.Memory.Attribution.Actor != "" || cleared.Memory.Attribution.Status != "unknown" {
				t.Fatalf("clear title/body: %+v %v", cleared, err)
			}
			assertOwnedLink(t, cleared.Memory, changedLink.Link)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = OpenExisting(ctx, o)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := s.Show(ctx, "beads/plan"); err != nil || !reflect.DeepEqual(got, cleared.Memory) {
				t.Fatalf("reopened current: %+v %v", got, err)
			}
			for _, want := range []Record{original, owned, updated.Memory, changedLink.Source.(Record), cleared.Memory} {
				got, err := s.ReadVersion(ctx, "beads/plan", want.Version)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("retained %s: %+v %v", want.Version, got, err)
				}
			}
			var count int
			if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM graph_preview_versions WHERE path='beads/plan'`).Scan(&count); err != nil || count != 5 {
				t.Fatalf("versions=%d want5: %v", count, err)
			}
		})
	}
}

func TestMemoryUpdateRefusalAndRollback(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, o := issueExperimentOptions(t, backend)
			s, err := OpenExisting(ctx, o)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := s.Close(); err != nil {
					t.Error(err)
				}
			}()
			memory, err := s.Create(ctx, CreateRequest{Path: "beads/plan", Title: "Before", Body: "before"})
			if err != nil {
				t.Fatal(err)
			}
			issue, err := s.CreateIssue(ctx, "beads/work", plainIssue("Work"))
			if err != nil {
				t.Fatal(err)
			}
			added, err := s.AddInformationalLink(ctx, LinkCreateRequest{Path: "links/out", SourcePath: "beads/plan", TargetPath: "beads/work", ExpectedSourceRevision: memory.Revision})
			if err != nil {
				t.Fatal(err)
			}
			request := MemoryUpdateRequest{Path: "beads/plan", Properties: Properties{Title: "After", Body: "after"}, ExpectedRevision: added.Source.(Record).Revision}
			before := workflowState(t, ctx, s)
			for _, tc := range []struct {
				name    string
				request MemoryUpdateRequest
				want    error
			}{
				{"missing-guard", MemoryUpdateRequest{Path: request.Path, Properties: request.Properties}, storage.ErrValidation},
				{"both-guards", MemoryUpdateRequest{Path: request.Path, Properties: request.Properties, ExpectedRevision: request.ExpectedRevision, Unconditional: true}, storage.ErrValidation},
				{"stale", MemoryUpdateRequest{Path: request.Path, Properties: request.Properties, ExpectedRevision: "stale"}, ErrConflict},
				{"issue", MemoryUpdateRequest{Path: "beads/work", Properties: request.Properties, ExpectedRevision: issue.Revision}, ErrCapabilityUnavailable},
				{"missing", MemoryUpdateRequest{Path: "beads/missing", Unconditional: true}, ErrNotFound},
				{"path", MemoryUpdateRequest{Path: "links/out", Unconditional: true}, storage.ErrValidation},
				{"title-unicode", MemoryUpdateRequest{Path: request.Path, Properties: Properties{Title: string([]byte{255})}, Unconditional: true}, storage.ErrValidation},
				{"body-unicode", MemoryUpdateRequest{Path: request.Path, Properties: Properties{Body: string([]byte{255})}, Unconditional: true}, storage.ErrValidation},
				{"actor-unicode", MemoryUpdateRequest{Path: request.Path, Actor: string([]byte{255}), Unconditional: true}, storage.ErrValidation},
			} {
				got, err := s.UpdateMemory(ctx, tc.request)
				if !errors.Is(err, tc.want) || !reflect.DeepEqual(got, MemoryMutationResult{}) {
					t.Fatalf("%s: %+v %v", tc.name, got, err)
				}
			}
			for _, stage := range []string{"coordination", "memory-payload", "source-catalog", "source-retained"} {
				fault := errors.New("injected memory update failure")
				s.afterWrite = func(at string) error {
					if at == stage {
						return fault
					}
					return nil
				}
				got, err := s.UpdateMemory(ctx, request)
				s.afterWrite = nil
				if !errors.Is(err, fault) || !reflect.DeepEqual(got, MemoryMutationResult{}) {
					t.Fatalf("%s: %+v %v", stage, got, err)
				}
				if !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
					t.Fatalf("%s leaked state", stage)
				}
			}
			canceled, cancel := context.WithCancel(ctx)
			s.afterWrite = func(stage string) error {
				if stage == "memory-payload" {
					cancel()
					return canceled.Err()
				}
				return nil
			}
			got, err := s.UpdateMemory(canceled, request)
			cancel()
			s.afterWrite = nil
			if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, MemoryMutationResult{}) {
				t.Fatalf("cancellation: %+v %v", got, err)
			}
			if !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
				t.Fatal("cancellation or refused request leaked state")
			}
			s.options.Binding.AuthorityID = "ffffffffffffffffffffffffffffffff"
			_, err = s.UpdateMemory(ctx, request)
			s.options = o
			if !errors.Is(err, ErrInvalidStore) {
				t.Fatalf("wrong authority: %v", err)
			}
			if !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
				t.Fatal("authority refusal leaked state")
			}
			if _, err := s.db.ExecContext(ctx, `UPDATE graph_preview_versions SET snapshot='{}' WHERE path=? AND version=?`, request.Path, request.ExpectedRevision); err != nil {
				t.Fatal(err)
			}
			corrupt := workflowState(t, ctx, s)
			if _, err := s.UpdateMemory(ctx, request); !errors.Is(err, ErrInvalidStore) {
				t.Fatalf("retained corruption: %v", err)
			}
			if !reflect.DeepEqual(corrupt, workflowState(t, ctx, s)) {
				t.Fatal("update repaired or changed corrupt state")
			}
		})
	}
}

func TestMemoryUpdateConcurrentWriters(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		for _, other := range []string{"memory", "owned-link"} {
			t.Run(backend+"/"+other, func(t *testing.T) {
				ctx, o := issueExperimentOptions(t, backend)
				ctx, cancel := context.WithCancel(ctx)
				defer cancel()
				first, err := OpenExisting(ctx, o)
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := first.Close(); err != nil {
						t.Error(err)
					}
				}()
				memory, err := first.Create(ctx, CreateRequest{Path: "beads/plan", Body: "original"})
				if err != nil {
					t.Fatal(err)
				}
				target, err := first.Create(ctx, CreateRequest{Path: "beads/target", Body: "target"})
				if err != nil {
					t.Fatal(err)
				}
				link, err := first.AddInformationalLink(ctx, LinkCreateRequest{Path: "links/out", SourcePath: "beads/plan", TargetPath: "beads/target", ExpectedSourceRevision: memory.Revision})
				if err != nil {
					t.Fatal(err)
				}
				source := link.Source.(Record)
				var second *Store
				if backend == "embedded" {
					// Concurrent callers share the production one-session embedded
					// pool. The second transaction observes a stale guard after the
					// first commits; this does not qualify overlapping SQL writes.
					second = &Store{db: first.db, options: o}
				} else {
					second, err = OpenExisting(ctx, o)
					if err != nil {
						t.Fatal(err)
					}
					defer func() {
						if err := second.Close(); err != nil {
							t.Error(err)
						}
					}()
				}
				reached := make(chan struct{}, 2)
				release := make(chan struct{})
				results := make(chan error, 2)
				pause := func(stage string) error {
					if backend != "server" || stage != "source-retained" {
						return nil
					}
					reached <- struct{}{}
					select {
					case <-release:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				first.afterWrite, second.afterWrite = pause, pause
				var writers sync.WaitGroup
				writers.Add(2)
				defer func() {
					cancel()
					writers.Wait()
				}()
				go func() {
					defer writers.Done()
					_, err := first.UpdateMemory(ctx, MemoryUpdateRequest{Path: "beads/plan", Properties: Properties{Body: "writer-one"}, ExpectedRevision: source.Revision})
					results <- err
				}()
				go func() {
					defer writers.Done()
					var err error
					if other == "memory" {
						_, err = second.UpdateMemory(ctx, MemoryUpdateRequest{Path: "beads/plan", Properties: Properties{Body: "writer-two"}, ExpectedRevision: source.Revision})
					} else {
						_, err = second.UpdateLink(ctx, LinkUpdateRequest{Path: "links/out", Properties: map[string]any{"note": "writer-two"}, ExpectedRevision: link.Link.Revision, ExpectedSourceRevision: source.Revision})
					}
					results <- err
				}()
				if backend == "server" {
					for range 2 {
						select {
						case <-reached:
						case err := <-results:
							cancel()
							t.Fatalf("writer before forced overlap: %v", err)
						case <-ctx.Done():
							t.Fatal(ctx.Err())
						}
					}
					close(release)
				}
				successes, conflicts := 0, 0
				for range 2 {
					select {
					case err := <-results:
						if err == nil {
							successes++
						} else if errors.Is(err, ErrConflict) && !errors.Is(err, ErrOutcomeUnknown) {
							conflicts++
						} else {
							t.Fatalf("writer: %v", err)
						}
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				}
				first.afterWrite, second.afterWrite = nil, nil
				if successes != 1 || conflicts != 1 {
					t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
				}
				current, err := first.Show(ctx, "beads/plan")
				if err != nil {
					t.Fatal(err)
				}
				currentLink, err := first.ShowLink(ctx, "links/out")
				if err != nil {
					t.Fatal(err)
				}
				assertOwnedLink(t, current, currentLink)
				if other == "memory" {
					if current.Properties.Body != "writer-one" && current.Properties.Body != "writer-two" {
						t.Fatalf("lost winning edit: %+v", current)
					}
					if !reflect.DeepEqual(currentLink, link.Link) {
						t.Fatal("content race changed Link")
					}
				} else if current.Properties.Body == "writer-one" {
					if !reflect.DeepEqual(currentLink, link.Link) {
						t.Fatal("both effects committed")
					}
				} else if current.Properties.Body != "original" || currentLink.Properties["note"] != "writer-two" || currentLink.Revision == link.Link.Revision {
					t.Fatalf("incomplete owned-Link winner: %+v %+v", current, currentLink)
				}
				if got, err := first.Show(ctx, "beads/target"); err != nil || !reflect.DeepEqual(got, target) {
					t.Fatalf("race changed target: %+v %v", got, err)
				}
				if got, err := first.ReadVersion(ctx, "beads/plan", source.Version); err != nil || !reflect.DeepEqual(got, source) {
					t.Fatalf("race changed old snapshot: %+v %v", got, err)
				}
				var count int
				if err := first.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM graph_preview_versions WHERE path='beads/plan'`).Scan(&count); err != nil || count != 3 {
					t.Fatalf("Memory versions=%d want3: %v", count, err)
				}
			})
		}
	}
}
