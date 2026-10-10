//go:build cgo

package graphstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
)

func TestLinkUnlinkAndIncidentLifecycle(t *testing.T) {
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
			source, err := s.Create(ctx, CreateRequest{Path: "beads/source"})
			if err != nil {
				t.Fatal(err)
			}
			target, err := s.Create(ctx, CreateRequest{Path: "beads/target"})
			if err != nil {
				t.Fatal(err)
			}
			issue, err := s.CreateIssue(ctx, "beads/work", plainIssue("Work"))
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.CreateIssue(ctx, "beads/prereq", plainIssue("Prerequisite"))
			if err != nil {
				t.Fatal(err)
			}
			dep, err := s.AddDependency(ctx, DependencyRequest{Path: "links/block", SourcePath: "beads/work", TargetPath: "beads/prereq", Actor: "author"})
			if err != nil {
				t.Fatal(err)
			}
			issue = dep.Source
			info, err := s.AddInformationalLink(ctx, LinkCreateRequest{Path: "links/context", SourcePath: "beads/work", TargetPath: "beads/source"})
			if err != nil {
				t.Fatal(err)
			}
			assertIncident(t, ctx, s, LinksRequest{BeadPath: "beads/work"}, dep.Link, info.Link)
			assertIncident(t, ctx, s, LinksRequest{BeadPath: "beads/work", Direction: "in"})
			assertIncident(t, ctx, s, LinksRequest{BeadPath: "beads/work", TypeURL: RelatedTypeURL(o.Binding.ScopeURL)}, info.Link)
			if _, err := s.Unlink(ctx, LinkDeleteRequest{SourcePath: "beads/work", TargetPath: "beads/prereq", TypeURL: DependencyTypeURL(o.Binding.ScopeURL), Actor: "author", Unconditional: true, UnconditionalSource: true}); !errors.Is(err, ErrCapabilityUnavailable) {
				t.Fatalf("blocking removal: %v", err)
			}
			if _, err := s.ListLinks(ctx, LinksRequest{BeadPath: "beads/work", Direction: "other"}); !errors.Is(err, storage.ErrValidation) {
				t.Fatalf("direction: %v", err)
			}
			if _, err := s.ListLinks(ctx, LinksRequest{BeadPath: "beads/work", TypeURL: "unknown"}); !errors.Is(err, storage.ErrValidation) {
				t.Fatalf("Type: %v", err)
			}
			links := []LinkRecord{}
			for _, path := range []string{"links/a", "links/z"} {
				added, err := s.AddInformationalLink(ctx, LinkCreateRequest{Path: path, SourcePath: "beads/source", TargetPath: "beads/target", ExpectedSourceRevision: source.Revision})
				if err != nil {
					t.Fatal(err)
				}
				source = added.Source.(Record)
				links = append(links, added.Link)
			}
			before := workflowState(t, ctx, s)
			pair := LinkDeleteRequest{SourcePath: "beads/source", TargetPath: "beads/target", TypeURL: RelatedTypeURL(o.Binding.ScopeURL), Unconditional: true, ExpectedSourceRevision: source.Revision, Actor: "deleter"}
			var ambiguous *ErrAmbiguousLink
			if _, err := s.Unlink(ctx, pair); !errors.As(err, &ambiguous) || !reflect.DeepEqual(ambiguous.CandidateIDs, []string{links[0].ID, links[1].ID}) {
				t.Fatalf("ambiguity: %+v %v", ambiguous, err)
			}
			req := LinkDeleteRequest{Path: "links/a", ExpectedRevision: links[0].Revision, ExpectedSourceRevision: source.Revision, Actor: "deleter"}
			bad := req
			bad.ExpectedRevision = "stale"
			if _, err := s.Unlink(ctx, bad); !errors.Is(err, ErrConflict) {
				t.Fatalf("stale Link: %v", err)
			}
			bad = req
			bad.ExpectedSourceRevision = "stale"
			if _, err := s.Unlink(ctx, bad); !errors.Is(err, ErrConflict) {
				t.Fatalf("stale source: %v", err)
			}
			bad = req
			bad.ExpectedRevision = ""
			if _, err := s.Unlink(ctx, bad); !errors.Is(err, storage.ErrValidation) {
				t.Fatalf("missing Link guard: %v", err)
			}
			bad = req
			bad.ExpectedSourceRevision = ""
			if _, err := s.Unlink(ctx, bad); !errors.Is(err, storage.ErrValidation) {
				t.Fatalf("missing source guard: %v", err)
			}
			if !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
				t.Fatal("refusal leaked state")
			}
			for _, stage := range []string{"coordination", "link-catalog", "link-payload", "link-retained", "source-catalog", "source-retained"} {
				fault := errors.New("unlink rollback")
				s.afterWrite = func(at string) error {
					if at == stage {
						return fault
					}
					return nil
				}
				_, err := s.Unlink(ctx, req)
				s.afterWrite = nil
				if !errors.Is(err, fault) {
					t.Fatalf("%s: %v", stage, err)
				}
				if !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
					t.Fatalf("%s leaked state", stage)
				}
			}
			removed, err := s.Unlink(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			current := removed.Source.(Record)
			if !removed.Changed || removed.Link.ID != links[0].ID || removed.Link.State != "deleted" || removed.Link.PreviousVersion != links[0].Version || removed.Link.Version == links[0].Version || current.Version == source.Version {
				t.Fatalf("removed: %+v", removed)
			}
			assertOwnedLink(t, current, links[1])
			assertRetainedMemory(t, ctx, s, "beads/source", source)
			assertIncident(t, ctx, s, LinksRequest{BeadPath: "beads/source"}, info.Link, links[1])
			if got, err := s.Show(ctx, "beads/target"); err != nil || !reflect.DeepEqual(got, target) {
				t.Fatalf("target changed: %v", err)
			}
			before = workflowState(t, ctx, s)
			if _, err := s.Read(ctx, "links/a"); !errors.Is(err, ErrGone) {
				t.Fatalf("deleted read: %v", err)
			}
			if _, err := s.Unlink(ctx, req); !errors.Is(err, ErrGone) {
				t.Fatalf("deleted unlink: %v", err)
			}
			if _, err := s.UpdateLink(ctx, LinkUpdateRequest{Path: "links/a", Unconditional: true, UnconditionalSource: true}); !errors.Is(err, ErrGone) {
				t.Fatalf("deleted update: %v", err)
			}
			if _, err := s.AddInformationalLink(ctx, LinkCreateRequest{Path: "links/a", SourcePath: "beads/source", TargetPath: "beads/target", UnconditionalSource: true}); !errors.Is(err, ErrAlreadyExists) {
				t.Fatalf("identity reuse: %v", err)
			}
			if !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
				t.Fatal("gone/refused reuse leaked state")
			}
			pair.ExpectedSourceRevision = current.Revision
			pair.Unconditional = false
			pair.ExpectedRevision = links[1].Revision
			removed, err = s.Unlink(ctx, pair)
			if err != nil || len(removed.Source.(Record).Owned) != 0 {
				t.Fatalf("exact pair removal: %+v %v", removed, err)
			}
			if _, err := s.Unlink(ctx, pair); !errors.Is(err, ErrNotFound) {
				t.Fatalf("empty pair: %v", err)
			}
			deletedInfo, err := s.Unlink(ctx, LinkDeleteRequest{Path: "links/context", ExpectedRevision: info.Link.Revision})
			if err != nil || !reflect.DeepEqual(deletedInfo.Source, issue) {
				t.Fatalf("unowned deletion changed Issue: %v", err)
			}
			assertIncident(t, ctx, s, LinksRequest{BeadPath: "beads/work"}, dep.Link)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = OpenExisting(ctx, o)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.ShowLink(ctx, "links/a"); !errors.Is(err, ErrGone) {
				t.Fatalf("reopen gone: %v", err)
			}
			var raw []byte
			if err := s.db.QueryRowContext(ctx, `SELECT snapshot FROM graph_preview_versions WHERE path=? AND version=?`, "links/a", links[0].Version).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var retained LinkRecord
			if json.Unmarshal(raw, &retained) != nil || !reflect.DeepEqual(retained, links[0]) {
				t.Fatal("prior Link snapshot changed")
			}
			// Delete the actual tombstone to prove corruption cannot masquerade as gone.
			if _, err := s.db.ExecContext(ctx, `DELETE FROM graph_preview_versions WHERE path='links/a' AND version=(SELECT revision FROM graph_preview_catalog WHERE path='links/a')`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ShowLink(ctx, "links/a"); !errors.Is(err, ErrInvalidStore) {
				t.Fatalf("corrupt deletion: %v", err)
			}
		})
	}
}

func assertIncident(t *testing.T, ctx context.Context, s *Store, request LinksRequest, want ...LinkRecord) {
	t.Helper()
	got, err := s.ListLinks(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	// Expected inputs are sorted below solely for the assertion; production order
	// is checked independently and uses canonical UTF-16 ordering.
	expected := map[string]LinkRecord{}
	for _, link := range want {
		expected[link.ID] = link
	}
	if len(got) != len(want) {
		t.Fatalf("incident count %d want %d", len(got), len(want))
	}
	for i, link := range got {
		if !reflect.DeepEqual(link, expected[link.ID]) {
			t.Fatalf("unexpected Link %+v", link)
		}
		if i > 0 && strings.Compare(got[i-1].ID, link.ID) >= 0 {
			t.Fatal("incident order")
		}
	}
}

func TestLinkUnlinkSelf(t *testing.T) {
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
			memory, err := s.Create(ctx, CreateRequest{Path: "beads/self"})
			if err != nil {
				t.Fatal(err)
			}
			created, err := s.AddInformationalLink(ctx, LinkCreateRequest{Path: "links/self", SourcePath: "beads/self", TargetPath: "beads/self", ExpectedSourceRevision: memory.Revision})
			if err != nil {
				t.Fatal(err)
			}
			for _, direction := range []string{"in", "out", "both"} {
				assertIncident(t, ctx, s, LinksRequest{BeadPath: "beads/self", Direction: direction}, created.Link)
			}
			source := created.Source.(Record)
			removed, err := s.Unlink(ctx, LinkDeleteRequest{Path: "links/self", ExpectedRevision: created.Link.Revision, ExpectedSourceRevision: source.Revision})
			if err != nil {
				t.Fatal(err)
			}
			if len(removed.Source.(Record).Owned) != 0 || removed.Source.(Record).Version == source.Version {
				t.Fatal("self deletion failed")
			}
			assertRetainedMemory(t, ctx, s, "beads/self", source)
			assertIncident(t, ctx, s, LinksRequest{BeadPath: "beads/self"})
			var count int
			if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM graph_preview_versions WHERE path='beads/self'`).Scan(&count); err != nil || count != 3 {
				t.Fatalf("self versions %d: %v", count, err)
			}
		})
	}
}

func TestLinkUnlinkConcurrentMutations(t *testing.T) {
	for _, mutation := range []string{"create", "update"} {
		for _, backend := range []string{"embedded", "server"} {
			t.Run(backend+"/"+mutation, func(t *testing.T) {
				ctx, o := issueExperimentOptions(t, backend)
				ctx, cancel := context.WithCancel(ctx)
				defer cancel()
				s, err := OpenExisting(ctx, o)
				if err != nil {
					t.Fatal(err)
				}
				memory, err := s.Create(ctx, CreateRequest{Path: "beads/source"})
				if err != nil {
					t.Fatal(err)
				}
				target, err := s.Create(ctx, CreateRequest{Path: "beads/target"})
				if err != nil {
					t.Fatal(err)
				}
				added, err := s.AddInformationalLink(ctx, LinkCreateRequest{Path: "links/one", SourcePath: "beads/source", TargetPath: "beads/target", ExpectedSourceRevision: memory.Revision})
				if err != nil {
					t.Fatal(err)
				}
				source := added.Source.(Record)
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				reached := make(chan struct{}, 2)
				release := make(chan struct{})
				results := make(chan error, 2)
				for _, operation := range []string{"unlink", mutation} {
					go func() {
						worker, err := OpenExisting(ctx, o)
						if err != nil {
							results <- err
							return
						}
						worker.afterWrite = func(stage string) error {
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
						switch operation {
						case "unlink":
							_, err = worker.Unlink(ctx, LinkDeleteRequest{SourcePath: "beads/source", TargetPath: "beads/target", TypeURL: RelatedTypeURL(o.Binding.ScopeURL), ExpectedRevision: added.Link.Revision, ExpectedSourceRevision: source.Revision})
						case "create":
							_, err = worker.AddInformationalLink(ctx, LinkCreateRequest{Path: "links/two", SourcePath: "beads/source", TargetPath: "beads/target", ExpectedSourceRevision: source.Revision})
						case "update":
							_, err = worker.UpdateLink(ctx, LinkUpdateRequest{Path: "links/one", Properties: map[string]any{"note": "raced"}, ExpectedRevision: added.Link.Revision, ExpectedSourceRevision: source.Revision})
						}
						results <- errors.Join(err, worker.Close())
					}()
				}
				if backend == "server" {
					for range 2 {
						select {
						case <-reached:
						case err := <-results:
							t.Fatalf("writer before forced overlap: %v", err)
						case <-ctx.Done():
							t.Fatal(ctx.Err())
						}
					}
					close(release)
				}
				successes, refusals := 0, 0
				for range 2 {
					err := <-results
					if errors.Is(err, ErrOutcomeUnknown) {
						t.Fatalf("unqualified uncertain writer: %v", err)
					}
					if err == nil {
						successes++
					} else if errors.Is(err, ErrConflict) || errors.Is(err, ErrGone) {
						refusals++
					} else {
						var ambiguous *ErrAmbiguousLink
						if errors.As(err, &ambiguous) {
							refusals++
						} else {
							t.Fatal(err)
						}
					}
				}
				if successes != 1 || refusals != 1 {
					t.Fatalf("successes=%d refusals=%d", successes, refusals)
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
				now, err := current.Show(ctx, "beads/source")
				if err != nil {
					t.Fatal(err)
				}
				incident, err := current.ListLinks(ctx, LinksRequest{BeadPath: "beads/source"})
				if err != nil {
					t.Fatal(err)
				}
				if len(incident) != len(now.Owned) {
					t.Fatal("owned/list state differs")
				}
				if len(now.Owned) == 0 {
					if _, err := current.ShowLink(ctx, "links/one"); !errors.Is(err, ErrGone) {
						t.Fatalf("winning unlink: %v", err)
					}
				} else if mutation == "create" && len(now.Owned) != 2 {
					t.Fatal("winning create lost Link")
				} else if mutation == "update" && (len(now.Owned) != 1 || incident[0].Properties["note"] != "raced") {
					t.Fatal("winning update lost payload")
				}
				if got, err := current.Show(ctx, "beads/target"); err != nil || !reflect.DeepEqual(got, target) {
					t.Fatalf("target changed: %v", err)
				}
				assertRetainedMemory(t, ctx, current, "beads/source", source)
				var count int
				if err := current.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM graph_preview_versions`).Scan(&count); err != nil || count != 6 {
					t.Fatalf("retained count=%d want6: %v", count, err)
				}
			})
		}
	}
}

func TestIncidentMappingCorruptionRefuses(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		for _, corruption := range []string{"informational-catalog", "informational-payload", "informational-type", "dependency-catalog", "dependency-payload"} {
			t.Run(backend+"/"+corruption, func(t *testing.T) {
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
				for _, path := range []string{"beads/source", "beads/target"} {
					if _, err := s.CreateIssue(ctx, path, plainIssue(path)); err != nil {
						t.Fatal(err)
					}
				}
				typ := RelatedTypeURL(o.Binding.ScopeURL)
				if strings.HasPrefix(corruption, "dependency") {
					_, err = s.AddDependency(ctx, DependencyRequest{Path: "links/test", SourcePath: "beads/source", TargetPath: "beads/target", Actor: "author"})
					typ = DependencyTypeURL(o.Binding.ScopeURL)
				} else {
					_, err = s.AddInformationalLink(ctx, LinkCreateRequest{Path: "links/test", SourcePath: "beads/source", TargetPath: "beads/target"})
				}
				if err != nil {
					t.Fatal(err)
				}
				query := map[string]string{
					"informational-catalog": `DELETE FROM graph_preview_catalog WHERE path='links/test'`,
					"informational-payload": `DELETE FROM graph_preview_links WHERE path='links/test'`,
					"informational-type":    `UPDATE graph_preview_catalog SET type_url='https://wrong.example/type' WHERE path='links/test'`,
					"dependency-catalog":    `DELETE FROM graph_preview_catalog WHERE path='links/test'`,
					"dependency-payload":    `DELETE FROM dependencies`,
				}[corruption]
				if _, err := s.db.ExecContext(ctx, query); err != nil {
					t.Fatal(err)
				}
				if _, err := s.ListLinks(ctx, LinksRequest{BeadPath: "beads/target", Direction: "in", TypeURL: typ}); !errors.Is(err, ErrInvalidStore) {
					t.Fatalf("incomplete listing: %v", err)
				}

				// Blocking pair selection is outside the preview capability and
				// refuses before reading storage, even when that storage is corrupt.
				wantPairError := ErrInvalidStore
				if typ == DependencyTypeURL(o.Binding.ScopeURL) {
					wantPairError = ErrCapabilityUnavailable
				}
				if _, err := s.Unlink(ctx, LinkDeleteRequest{SourcePath: "beads/source", TargetPath: "beads/target", TypeURL: typ, Unconditional: true, UnconditionalSource: true}); !errors.Is(err, wantPairError) {
					t.Fatalf("corrupt pair selection: %v", err)
				}
				if corruption == "dependency-payload" {
					if _, err := s.Unlink(ctx, LinkDeleteRequest{Path: "links/test", Unconditional: true, UnconditionalSource: true, Actor: "author"}); !errors.Is(err, ErrInvalidStore) {
						t.Fatalf("corrupt canonical selection: %v", err)
					}
				}
			})
		}
	}
}

// This synthetic storage-boundary test inserts complete unowned Link records in
// one transaction to reach the preview budget cheaply. Installed qualification
// separately uses only normal CLI writes; this is not a demonstration fixture.
func TestIncidentLinkBudgetAndExactPair(t *testing.T) {
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
			if _, err := s.CreateIssue(ctx, "beads/source", plainIssue("Source")); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"beads/common", "beads/unique"} {
				if _, err := s.Create(ctx, CreateRequest{Path: path}); err != nil {
					t.Fatal(err)
				}
			}
			unique, err := s.AddInformationalLink(ctx, LinkCreateRequest{Path: "links/unique", SourcePath: "beads/source", TargetPath: "beads/unique"})
			if err != nil {
				t.Fatal(err)
			}
			template, err := s.AddInformationalLink(ctx, LinkCreateRequest{Path: "links/common-0", SourcePath: "beads/source", TargetPath: "beads/common"})
			if err != nil {
				t.Fatal(err)
			}
			tx, err := s.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			for i := 1; i < PreviewIncidentLinkLimit; i++ {
				path := fmt.Sprintf("links/common-%d", i)
				link := template.Link
				link.ID = o.Binding.ScopeURL + path
				snapshot, err := canonicalJSON(link)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO graph_preview_catalog(path,resource_kind,type_url,revision,allocation_state,backing) VALUES(?,'link',?,?,'live','informational')`, path, link.Type, link.Revision); err != nil {
					t.Fatal(err)
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO graph_preview_links(path,source_path,target_path,properties,metadata,attribution) SELECT ?,'beads/source','beads/common',properties,metadata,attribution FROM graph_preview_links WHERE path='links/common-0'`, path); err != nil {
					t.Fatal(err)
				}
				// Each path is fresh, so this is its first version: ordinal 1.
				if _, err := tx.ExecContext(ctx, `INSERT INTO graph_preview_versions(path,version,snapshot,actor,ordinal,change_at) VALUES(?,?,?,'',1,?)`, path, link.Version, snapshot, time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if links, err := s.ListLinks(ctx, LinksRequest{BeadPath: "beads/source"}); !errors.Is(err, ErrLimitExceeded) || links != nil {
				t.Fatalf("truncated instead of refused: %d %v", len(links), err)
			}
			exact, err := s.Unlink(ctx, LinkDeleteRequest{SourcePath: "beads/source", TargetPath: "beads/unique", TypeURL: RelatedTypeURL(o.Binding.ScopeURL), ExpectedRevision: unique.Link.Revision})
			if err != nil || exact.Link.ID != unique.Link.ID {
				t.Fatalf("unrelated Links blocked exact pair: %+v %v", exact, err)
			}
			links, err := s.ListLinks(ctx, LinksRequest{BeadPath: "beads/source"})
			if err != nil || len(links) != PreviewIncidentLinkLimit {
				t.Fatalf("exact budget: %d %v", len(links), err)
			}
		})
	}
}

func TestDeletedLinkPriorStateIntegrity(t *testing.T) {
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
			if _, err := s.CreateIssue(ctx, "beads/source", plainIssue("Source")); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Create(ctx, CreateRequest{Path: "beads/target"}); err != nil {
				t.Fatal(err)
			}
			added, err := s.AddInformationalLink(ctx, LinkCreateRequest{Path: "links/prior", SourcePath: "beads/source", TargetPath: "beads/target", Actor: "author"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Unlink(ctx, LinkDeleteRequest{Path: "links/prior", ExpectedRevision: added.Link.Revision}); err != nil {
				t.Fatal(err)
			}
			for _, corruption := range []string{"missing-endpoint", "invalid-property", "invalid-attribution", "actor-mismatch"} {
				t.Run(corruption, func(t *testing.T) {
					link := added.Link
					actor := link.Attribution.Actor
					switch corruption {
					case "missing-endpoint":
						link.Source = ""
					case "invalid-property":
						link.Properties = map[string]any{"note": 42}
					case "invalid-attribution":
						link.Attribution.Status = "verified"
					case "actor-mismatch":
						actor = "other"
					}
					snapshot, err := canonicalJSON(link)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := s.db.ExecContext(ctx, `UPDATE graph_preview_versions SET snapshot=?,actor=? WHERE path='links/prior' AND version=?`, snapshot, actor, added.Link.Version); err != nil {
						t.Fatal(err)
					}
					if _, err := s.ShowLink(ctx, "links/prior"); !errors.Is(err, ErrInvalidStore) {
						t.Fatalf("corruption masquerades as gone: %v", err)
					}
				})
			}
		})
	}
}

// Whole-workspace reads may refuse oversized state, but subject-local unlink
// must still permit shrinking recovery. Default source acceptance must enter
// the existing transaction instead of adding a separate Read admission gate.
func TestLinkUnlinkDefaultSourceReadBudget(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			for _, selection := range []string{"id-default", "pair-default", "id-explicit"} {
				t.Run(selection, func(t *testing.T) {
					ctx, _, s, original, target, link := disclosureFixture(t, backend)
					filler, err := s.Create(ctx, CreateRequest{Path: "beads/filler", Body: strings.Repeat("f", 6<<20)})
					if err != nil {
						t.Fatal(err)
					}
					if _, err := s.Read(ctx, "links/out"); err != nil {
						t.Fatalf("fixture unreadable: %v", err)
					}
					replacement, err := s.UpdateLink(ctx, LinkUpdateRequest{Path: "links/out", Properties: map[string]any{"note": strings.Repeat("x", 768<<10)}, ExpectedRevision: link.Link.Revision, ExpectedSourceRevision: original.Revision, Actor: "replacement"})
					if err != nil || !replacement.Changed {
						t.Fatalf("oversized replacement: changed=%t err=%v", replacement.Changed, err)
					}
					source := replacement.Source.(Record)
					// Model oversized state left by a writer predating the budget guard.
					replaceLegacyFixtureMemory(t, ctx, s, "beads/filler", filler, strings.Repeat("f", PreviewCurrentReadByteLimit/2-(512<<10)))
					if _, err := s.Read(ctx, "links/out"); !errors.Is(err, ErrLimitExceeded) {
						t.Fatalf("fixture must exceed current-read budget: %v", err)
					}
					request := LinkDeleteRequest{Path: "links/out", ExpectedRevision: replacement.Link.Revision, DefaultInformationalSource: true, Actor: "recovery"}
					if selection == "pair-default" {
						request.Path = ""
						request.SourcePath, request.TargetPath, request.TypeURL = "beads/plan", "beads/target", RelatedTypeURL(s.ScopeURL())
					} else if selection == "id-explicit" {
						request.DefaultInformationalSource, request.UnconditionalSource = false, true
					}
					before := workflowState(t, ctx, s)
					for _, refusal := range []struct {
						name string
						edit func(*LinkDeleteRequest)
						want error
					}{
						{"stale-Link", func(r *LinkDeleteRequest) { r.ExpectedRevision = link.Link.Revision }, ErrConflict},
						{"missing-Link-guard", func(r *LinkDeleteRequest) { r.ExpectedRevision = "" }, storage.ErrValidation},
						{"stale-source", func(r *LinkDeleteRequest) {
							r.DefaultInformationalSource = false
							r.UnconditionalSource = false
							r.ExpectedSourceRevision = original.Revision
						}, ErrConflict},
						{"default-with-explicit-token", func(r *LinkDeleteRequest) {
							r.DefaultInformationalSource = true
							r.UnconditionalSource = false
							r.ExpectedSourceRevision = source.Revision
						}, storage.ErrValidation},
						{"default-with-explicit-unconditional", func(r *LinkDeleteRequest) { r.DefaultInformationalSource = true; r.UnconditionalSource = true }, storage.ErrValidation},
					} {
						bad := request
						refusal.edit(&bad)
						got, err := s.Unlink(ctx, bad)
						if !errors.Is(err, refusal.want) || !reflect.DeepEqual(got, LinkDeleteResult{}) || !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
							t.Fatalf("%s changed state or returned wrong refusal: %+v %v", refusal.name, got, err)
						}
					}
					got, err := s.Unlink(ctx, request)
					if err != nil || !got.Changed {
						t.Fatalf("unlink recovery: %+v %v", got, err)
					}
					assertReplacedMemory(t, got.ReplacedSource, source)
					if _, err := s.Read(ctx, "links/out"); !errors.Is(err, ErrGone) {
						t.Fatalf("deleted Link still readable: %v", err)
					}
					if current, err := s.Read(ctx, "beads/target"); err != nil || !reflect.DeepEqual(current, target) {
						t.Fatalf("unlink failed to restore reads or changed target: %+v %v", current, err)
					}
					if current, err := s.Read(ctx, "beads/plan"); err != nil || !reflect.DeepEqual(current, got.Source) {
						t.Fatalf("recovered source mismatch: %+v %v", current, err)
					}
					for path, version := range map[string]string{"links/out": replacement.Link.Version, "beads/plan": source.Version} {
						if _, err := s.ReadVersion(ctx, path, version); err != nil {
							t.Fatalf("lost retained %s: %v", path, err)
						}
					}
				})
			}
		})
	}
}
