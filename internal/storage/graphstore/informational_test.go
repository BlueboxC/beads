//go:build cgo

package graphstore

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

func TestInformationalLinkLifecycle(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
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
			issue, err := s.CreateIssue(ctx, "beads/work", plainIssue("Work"))
			if err != nil {
				t.Fatal(err)
			}
			source, err := s.Create(ctx, CreateRequest{Path: "beads/plan", Body: "Plan"})
			if err != nil {
				t.Fatal(err)
			}
			target, err := s.Create(ctx, CreateRequest{Path: "beads/notes", Body: "Notes"})
			if err != nil {
				t.Fatal(err)
			}
			issueLink, err := s.AddInformationalLink(ctx, LinkCreateRequest{SourcePath: "beads/work", TargetPath: "beads/plan", Path: "links/context", Properties: map[string]any{"note": "context"}})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(issueLink.Source, issue) {
				t.Fatal("unowned informational Link changed Issue")
			}
			updatedIssueLink, err := s.UpdateLink(ctx, LinkUpdateRequest{Path: "links/context", Properties: map[string]any{"note": "revised context"}, ExpectedRevision: issueLink.Link.Revision, ExpectedSourceRevision: issue.Revision})
			if err != nil || !updatedIssueLink.Changed || !reflect.DeepEqual(updatedIssueLink.Source, issue) {
				t.Fatalf("unowned update changed Issue: %+v %v", updatedIssueLink, err)
			}
			beforeUnowned := workflowState(t, ctx, s)
			if _, err := s.UpdateLink(ctx, LinkUpdateRequest{Path: "links/context", Properties: updatedIssueLink.Link.Properties, Unconditional: true, ExpectedSourceRevision: "stale"}); !errors.Is(err, ErrConflict) {
				t.Fatalf("unowned optional source guard: %v", err)
			}
			if !reflect.DeepEqual(beforeUnowned, workflowState(t, ctx, s)) {
				t.Fatal("unowned source refusal changed state")
			}
			assertReadyIDs(t, ctx, s, issue.ID)
			req := LinkCreateRequest{SourcePath: "beads/plan", TargetPath: "beads/notes", Path: "links/evidence", Properties: map[string]any{"note": "first"}, ExpectedSourceRevision: source.Revision, Actor: "author"}
			before := workflowState(t, ctx, s)
			missing := req
			missing.ExpectedSourceRevision = ""
			if _, err := s.AddInformationalLink(ctx, missing); !errors.Is(err, storage.ErrValidation) {
				t.Fatalf("missing source guard: %v", err)
			}
			bad := req
			bad.Properties = map[string]any{"gunk": true}
			if _, err := s.AddInformationalLink(ctx, bad); !errors.Is(err, storage.ErrValidation) {
				t.Fatalf("unknown property: %v", err)
			}
			for _, stage := range []string{"coordination", "link-catalog", "link-payload", "link-retained", "source-catalog", "source-retained"} {
				fault := errors.New("injected failure")
				s.afterWrite = func(at string) error {
					if at == stage {
						return fault
					}
					return nil
				}
				_, err := s.AddInformationalLink(ctx, req)
				s.afterWrite = nil
				if !errors.Is(err, fault) {
					t.Fatalf("create %s: %v", stage, err)
				}
				if !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
					t.Fatalf("create %s leaked state", stage)
				}
			}
			added, err := s.AddInformationalLink(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			owned := added.Source.(Record)
			if !added.Changed || owned.Version == source.Version || len(owned.Owned) != 1 {
				t.Fatalf("wrong owned result: %+v", added)
			}
			assertOwnedLink(t, owned, added.Link)
			if got, err := s.Show(ctx, "beads/notes"); err != nil || !reflect.DeepEqual(got, target) {
				t.Fatalf("incoming Link changed target: %v", err)
			}
			assertRetainedMemory(t, ctx, s, "beads/plan", source)
			assertRetainedMemory(t, ctx, s, "beads/plan", owned)
			update := LinkUpdateRequest{Path: req.Path, Properties: map[string]any{"note": "revised"}, Actor: "editor", ExpectedRevision: added.Link.Revision, ExpectedSourceRevision: owned.Revision}
			before = workflowState(t, ctx, s)
			for _, stage := range []string{"coordination", "link-catalog", "link-payload", "link-retained", "source-catalog", "source-retained"} {
				fault := errors.New("injected failure")
				s.afterWrite = func(at string) error {
					if at == stage {
						return fault
					}
					return nil
				}
				_, err := s.UpdateLink(ctx, update)
				s.afterWrite = nil
				if !errors.Is(err, fault) {
					t.Fatalf("update %s: %v", stage, err)
				}
				if !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
					t.Fatalf("update %s leaked state", stage)
				}
			}
			changed, err := s.UpdateLink(ctx, update)
			if err != nil {
				t.Fatal(err)
			}
			now := changed.Source.(Record)
			if !changed.Changed || changed.Link.Version == added.Link.Version || now.Version == owned.Version || changed.Link.Properties["note"] != "revised" {
				t.Fatalf("wrong update: %+v", changed)
			}
			assertOwnedLink(t, now, changed.Link)
			assertRetainedMemory(t, ctx, s, "beads/plan", owned)
			before = workflowState(t, ctx, s)
			stale := update
			stale.Properties = changed.Link.Properties
			if _, err := s.UpdateLink(ctx, stale); !errors.Is(err, ErrConflict) {
				t.Fatalf("stale Link noop guard: %v", err)
			}
			stale.ExpectedRevision = changed.Link.Revision
			if _, err := s.UpdateLink(ctx, stale); !errors.Is(err, ErrConflict) {
				t.Fatalf("stale source noop guard: %v", err)
			}
			update.ExpectedRevision = changed.Link.Revision
			update.ExpectedSourceRevision = now.Revision
			noop, err := s.UpdateLink(ctx, update)
			if err != nil || noop.Changed || !reflect.DeepEqual(noop.Link, changed.Link) || !reflect.DeepEqual(noop.Source, now) {
				t.Fatalf("noop: %+v %v", noop, err)
			}
			update.ExpectedRevision = ""
			if _, err := s.UpdateLink(ctx, update); !errors.Is(err, storage.ErrValidation) {
				t.Fatalf("missing Link guard: %v", err)
			}
			update.Unconditional = true
			update.ExpectedSourceRevision = ""
			if _, err := s.UpdateLink(ctx, update); !errors.Is(err, storage.ErrValidation) {
				t.Fatalf("missing source update guard: %v", err)
			}
			if !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
				t.Fatal("refusal/noop mutated state")
			}
			req.ExpectedSourceRevision = now.Revision
			if _, err := s.AddInformationalLink(ctx, req); !errors.Is(err, ErrAlreadyExists) {
				t.Fatalf("reused path: %v", err)
			}
			req.Path = "links/second"
			second, err := s.AddInformationalLink(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			if second.Link.ID == added.Link.ID || len(second.Source.(Record).Owned) != 2 {
				t.Fatal("equal endpoints deduplicated")
			}
			if got, err := s.Show(ctx, "beads/notes"); err != nil || !reflect.DeepEqual(got, target) {
				t.Fatalf("target changed: %v", err)
			}
			assertReadyIDs(t, ctx, s, issue.ID)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = OpenExisting(ctx, o)
			if err != nil {
				t.Fatal(err)
			}
			got, err := s.Read(ctx, req.Path)
			if err != nil || !reflect.DeepEqual(got, second.Link) {
				t.Fatalf("reopen Link differs: %v", err)
			}
			got, err = s.Read(ctx, "beads/plan")
			if err != nil || !reflect.DeepEqual(got, second.Source) {
				t.Fatalf("reopen Memory differs: %v", err)
			}
			// Tampering with authoritative Link data cannot be hidden by a retained copy.
			if _, err := s.db.ExecContext(ctx, `UPDATE graph_preview_links SET properties='{"note":"tampered"}' WHERE path=?`, req.Path); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ShowLink(ctx, req.Path); !errors.Is(err, ErrInvalidStore) {
				t.Fatalf("corrupt Link read: %v", err)
			}
			if _, err := s.Show(ctx, "beads/plan"); !errors.Is(err, ErrInvalidStore) {
				t.Fatalf("corrupt owned read: %v", err)
			}
		})
	}
}

func assertOwnedLink(t *testing.T, memory Record, link LinkRecord) {
	t.Helper()
	var owned LinkRecord
	if len(memory.Owned) != 1 {
		t.Fatalf("owned count %d", len(memory.Owned))
	}
	if err := json.Unmarshal(memory.Owned[0], &owned); err != nil || !reflect.DeepEqual(owned, link) {
		t.Fatalf("owned Link differs: %v", err)
	}
}
func assertRetainedMemory(t *testing.T, ctx context.Context, s *Store, path string, want Record) {
	t.Helper()
	var raw []byte
	if err := s.db.QueryRowContext(ctx, `SELECT snapshot FROM graph_preview_versions WHERE path=? AND version=?`, path, want.Version).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var got Record
	if err := json.Unmarshal(raw, &got); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("retained Memory changed: %v", err)
	}
}

func TestInformationalOwnedConcurrentMutations(t *testing.T) {
	for _, mutation := range []string{"create", "update"} {
		for _, backend := range []string{"embedded", "server"} {
			t.Run(backend+"/"+mutation, func(t *testing.T) {
				ctx, o := issueExperimentOptions(t, backend)
				ctx, cancel := context.WithCancel(ctx)
				defer cancel()
				initial, err := OpenExisting(ctx, o)
				if err != nil {
					t.Fatal(err)
				}
				source, err := initial.Create(ctx, CreateRequest{Path: "beads/source"})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := initial.Create(ctx, CreateRequest{Path: "beads/target"}); err != nil {
					t.Fatal(err)
				}
				links := map[string]LinkRecord{}
				if mutation == "update" {
					for _, path := range []string{"links/one", "links/two"} {
						added, err := initial.AddInformationalLink(ctx, LinkCreateRequest{Path: path, SourcePath: "beads/source", TargetPath: "beads/target", ExpectedSourceRevision: source.Revision})
						if err != nil {
							t.Fatal(err)
						}
						source = added.Source.(Record)
						links[path] = added.Link
					}
				}
				if err := initial.Close(); err != nil {
					t.Fatal(err)
				}
				reached := make(chan struct{}, 2)
				release := make(chan struct{})
				results := make(chan error, 2)
				for _, path := range []string{"links/one", "links/two"} {
					go func() {
						s, err := OpenExisting(ctx, o)
						if err != nil {
							results <- err
							return
						}
						s.afterWrite = func(stage string) error {
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
						if mutation == "create" {
							_, err = s.AddInformationalLink(ctx, LinkCreateRequest{Path: path, SourcePath: "beads/source", TargetPath: "beads/target", ExpectedSourceRevision: source.Revision})
						} else {
							_, err = s.UpdateLink(ctx, LinkUpdateRequest{Path: path, Properties: map[string]any{"note": "changed"}, ExpectedRevision: links[path].Revision, ExpectedSourceRevision: source.Revision})
						}
						results <- errors.Join(err, s.Close())
					}()
				}
				if backend == "server" {
					for range 2 {
						select {
						case <-reached:
						case err := <-results:
							t.Fatalf("writer before overlap: %v", err)
						case <-ctx.Done():
							t.Fatal(ctx.Err())
						}
					}
					close(release)
				}
				successes, conflicts := 0, 0
				for range 2 {
					err := <-results
					if err == nil {
						successes++
					} else if errors.Is(err, ErrConflict) && !errors.Is(err, ErrOutcomeUnknown) {
						conflicts++
					} else {
						t.Fatal(err)
					}
				}
				if successes != 1 || conflicts != 1 {
					t.Fatalf("success=%d conflict=%d", successes, conflicts)
				}
				current, err := OpenExisting(ctx, o)
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := current.Close(); err != nil {
						t.Error(err)
					}
				}()
				wantLinks, wantVersions := 1, 4
				if mutation == "update" {
					wantLinks, wantVersions = 2, 8
				}
				memory, err := current.Show(ctx, "beads/source")
				if err != nil || len(memory.Owned) != wantLinks {
					t.Fatalf("owned state after race: %+v %v", memory, err)
				}
				var n int
				if err := current.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM graph_preview_links`).Scan(&n); err != nil || n != wantLinks {
					t.Fatalf("leaked losing Link: %d %v", n, err)
				}
				if err := current.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM graph_preview_versions`).Scan(&n); err != nil || n != wantVersions {
					t.Fatalf("leaked losing retained state: %d %v", n, err)
				}
			})
		}
	}
}

// Self-links own one source snapshot, even though that source is also target.
// Endpoint validation must not recursively follow the owned Link back to itself.
func TestInformationalMemorySelfLink(t *testing.T) {
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
			memory, err := s.Create(ctx, CreateRequest{Path: "beads/self", Body: "Self reference"})
			if err != nil {
				t.Fatal(err)
			}
			created, err := s.AddInformationalLink(ctx, LinkCreateRequest{Path: "links/self", SourcePath: "beads/self", TargetPath: "beads/self", ExpectedSourceRevision: memory.Revision})
			if err != nil {
				t.Fatal(err)
			}
			source := created.Source.(Record)
			if !created.Changed || source.Version == memory.Version || created.Link.Source != memory.ID || created.Link.Target != memory.ID {
				t.Fatalf("self create: %+v", created)
			}
			assertOwnedLink(t, source, created.Link)
			assertRetainedMemory(t, ctx, s, "beads/self", memory)
			request := LinkUpdateRequest{Path: "links/self", Properties: map[string]any{"note": "self"}, ExpectedRevision: created.Link.Revision, ExpectedSourceRevision: source.Revision}
			changed, err := s.UpdateLink(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			current := changed.Source.(Record)
			if !changed.Changed || current.Version == source.Version || changed.Link.Version == created.Link.Version {
				t.Fatalf("self update: %+v", changed)
			}
			assertOwnedLink(t, current, changed.Link)
			assertRetainedMemory(t, ctx, s, "beads/self", source)
			got, err := s.Show(ctx, "beads/self")
			if err != nil || !reflect.DeepEqual(got, current) {
				t.Fatalf("self Memory read: %v", err)
			}
			gotLink, err := s.ShowLink(ctx, "links/self")
			if err != nil || !reflect.DeepEqual(gotLink, changed.Link) {
				t.Fatalf("self Link read: %v", err)
			}
			request.ExpectedRevision = changed.Link.Revision
			request.ExpectedSourceRevision = current.Revision
			before := workflowState(t, ctx, s)
			noop, err := s.UpdateLink(ctx, request)
			if err != nil || noop.Changed || !reflect.DeepEqual(noop.Source, current) || !reflect.DeepEqual(noop.Link, changed.Link) {
				t.Fatalf("self noop: %+v %v", noop, err)
			}
			if !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
				t.Fatal("self noop changed state")
			}
			for path, want := range map[string]int{"beads/self": 3, "links/self": 2} {
				var n int
				if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM graph_preview_versions WHERE path=?`, path).Scan(&n); err != nil || n != want {
					t.Fatalf("%s versions=%d want=%d: %v", path, n, want, err)
				}
			}
		})
	}
}
