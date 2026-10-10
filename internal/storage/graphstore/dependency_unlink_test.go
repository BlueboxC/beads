//go:build cgo

package graphstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

type dependencyUnlinkFixture struct {
	ctx     context.Context
	options Options
	store   *Store
	source  IssueRecord
	targets []IssueRecord
	links   []LinkRecord
}

func setupDependencyUnlink(t *testing.T, backend string, count int) dependencyUnlinkFixture {
	t.Helper()
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
	source, err := s.CreateIssue(ctx, "beads/source", plainIssue("Source"))
	if err != nil {
		t.Fatal(err)
	}
	f := dependencyUnlinkFixture{ctx: ctx, options: o, store: s, source: source}
	for i, path := range []string{"beads/first", "beads/second", "beads/third"} {
		target, err := s.CreateIssue(ctx, path, plainIssue(path))
		if err != nil {
			t.Fatal(err)
		}
		f.targets = append(f.targets, target)
		if i < count {
			added, err := s.AddDependency(ctx, DependencyRequest{Path: []string{"links/first", "links/second", "links/third"}[i], SourcePath: "beads/source", TargetPath: path, Actor: "author", ExpectedSourceRevision: f.source.Revision})
			if err != nil {
				t.Fatal(err)
			}
			f.source = added.Source
			f.links = append(f.links, added.Link)
		}
	}
	return f
}

func dependencyUnlinkRequest(link LinkRecord, source IssueRecord) LinkDeleteRequest {
	return LinkDeleteRequest{Path: strings.TrimPrefix(link.ID, strings.TrimSuffix(source.ID, "beads/source")), Actor: "remover", ExpectedRevision: link.Revision, ExpectedSourceRevision: source.Revision}
}

func dependencyRemovalCount(t *testing.T, f dependencyUnlinkFixture) int {
	t.Helper()
	var count int
	if err := f.store.db.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM events WHERE issue_id=? AND event_type='dependency_removed'", f.source.Properties.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func assertDependencyReadiness(t *testing.T, ctx context.Context, s *Store, source IssueRecord, want bool) {
	t.Helper()
	ready, err := s.ReadyIssues(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range ready {
		if item.ID == source.ID {
			found = true
		}
	}
	var blocked bool
	if err := s.db.QueryRowContext(ctx, "SELECT is_blocked FROM issues WHERE id=?", source.Properties.ID).Scan(&blocked); err != nil {
		t.Fatal(err)
	}
	if found != want || blocked == want {
		t.Fatalf("ready=%v blocked=%v wantReady=%v", found, blocked, want)
	}
}

func assertDependencyOwned(t *testing.T, source IssueRecord, links ...LinkRecord) {
	t.Helper()
	wanted := make([]json.RawMessage, 0, len(links))
	for _, link := range links {
		raw, err := canonicalJSON(link)
		if err != nil {
			t.Fatal(err)
		}
		wanted = append(wanted, raw)
	}
	if !reflect.DeepEqual(source.Owned, wanted) {
		t.Fatalf("owned=%s want=%s", source.Owned, wanted)
	}
}

func TestDependencyUnlinkLifecycle(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			f := setupDependencyUnlink(t, backend, 2)
			s, ctx := f.store, f.ctx
			memory, err := s.Create(ctx, CreateRequest{Path: "beads/context", Body: "Context"})
			if err != nil {
				t.Fatal(err)
			}
			info, err := s.AddInformationalLink(ctx, LinkCreateRequest{Path: "links/info", SourcePath: "beads/source", TargetPath: "beads/context", Actor: "author"})
			if err != nil {
				t.Fatal(err)
			}
			assertDependencyReadiness(t, ctx, s, f.source, false)
			original := f.source
			events := dependencyRemovalCount(t, f)
			for i, link := range f.links {
				prior := f.source
				deleted, err := s.Unlink(ctx, dependencyUnlinkRequest(link, prior))
				if err != nil {
					t.Fatal(err)
				}
				source, ok := deleted.Source.(IssueRecord)
				if !ok || !deleted.Changed || deleted.Link.State != "deleted" || deleted.Link.PreviousVersion != link.Version || deleted.Link.ID != link.ID || deleted.Link.Type != link.Type || deleted.Link.Version == link.Version || source.Revision == prior.Revision {
					t.Fatalf("removal: %+v", deleted)
				}
				if !source.Properties.UpdatedAt.Equal(prior.Properties.UpdatedAt) {
					t.Fatal("derived Dependency removal changed source updated_at")
				}
				if source.Attribution.Actor != "remover" || deleted.Link.Attribution.Actor != "remover" {
					t.Fatal("removal attribution lost")
				}
				f.source = source
				assertDependencyOwned(t, source, f.links[i+1:]...)
				assertDependencyReadiness(t, ctx, s, source, i == 1)
				assertIssueEditCounts(t, ctx, s, source.Properties.ID, 4+i)
				if dependencyRemovalCount(t, f) != events+i+1 {
					t.Fatal("removal event was missing or duplicated")
				}
				var key sql.NullString
				var state string
				if err := s.db.QueryRowContext(ctx, "SELECT backing_key,allocation_state FROM graph_preview_catalog WHERE path=?", dependencyUnlinkRequest(link, prior).Path).Scan(&key, &state); err != nil {
					t.Fatal(err)
				}
				if key.Valid || state != "deleted" {
					t.Fatalf("deleted allocation retained private key: %v %s", key, state)
				}
				if _, err := s.ShowLink(ctx, dependencyUnlinkRequest(link, prior).Path); !errors.Is(err, ErrGone) {
					t.Fatalf("current removed Link: %v", err)
				}
				if got, err := s.ReadVersion(ctx, dependencyUnlinkRequest(link, prior).Path, link.Version); err != nil || !reflect.DeepEqual(got, link) {
					t.Fatalf("old Link: %+v %v", got, err)
				}
				if _, err := s.ReadVersion(ctx, dependencyUnlinkRequest(link, prior).Path, deleted.Link.Version); !errors.Is(err, ErrGone) {
					t.Fatalf("tombstone version: %v", err)
				}
				assertIssueEditVersion(t, ctx, s, "beads/source", prior)
				// This landing has exact retained reads but no comparison API. Verify
				// both complete snapshots directly; source.Owned was checked above
				// against precisely the remaining Links, while prior retains the old set.
				assertIssueEditVersion(t, ctx, s, "beads/source", source)
				before := workflowState(t, ctx, s)
				if got, err := s.Unlink(ctx, dependencyUnlinkRequest(link, source)); !errors.Is(err, ErrGone) || !reflect.DeepEqual(got, LinkDeleteResult{}) {
					t.Fatalf("repeat: %+v %v", got, err)
				}
				if !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
					t.Fatal("repeat changed state")
				}
			}
			readded, err := s.AddDependency(ctx, DependencyRequest{Path: "links/readded", SourcePath: "beads/source", TargetPath: "beads/first", Actor: "author", ExpectedSourceRevision: f.source.Revision})
			if err != nil {
				t.Fatal(err)
			}
			assertDependencyOwned(t, readded.Source, readded.Link)
			assertDependencyReadiness(t, ctx, s, readded.Source, false)
			assertIssueEditCounts(t, ctx, s, readded.Source.Properties.ID, 6)
			before := workflowState(t, ctx, s)
			if _, err := s.AddDependency(ctx, DependencyRequest{Path: "links/first", SourcePath: "beads/source", TargetPath: "beads/third", Actor: "author", ExpectedSourceRevision: readded.Source.Revision}); !errors.Is(err, ErrAlreadyExists) {
				t.Fatalf("old identity reuse: %v", err)
			}
			if !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
				t.Fatal("identity refusal changed state")
			}
			if _, err := s.ShowLink(ctx, "links/first"); !errors.Is(err, ErrGone) {
				t.Fatalf("old ID resurrected: %v", err)
			}
			assertIncident(t, ctx, s, LinksRequest{BeadPath: "beads/source"}, info.Link, readded.Link)
			snap, err := s.CurrentSnapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, record := range snap.Records {
				if link, ok := record.(LinkRecord); ok && (link.ID == f.links[0].ID || link.ID == f.links[1].ID) {
					t.Fatal("deleted Link leaked into inventory")
				}
			}
			for i, path := range []string{"beads/first", "beads/second", "beads/third"} {
				if got, err := s.ShowIssue(ctx, path); err != nil || !reflect.DeepEqual(got, f.targets[i]) {
					t.Fatalf("target changed: %+v %v", got, err)
				}
			}
			if got, err := s.Show(ctx, "beads/context"); err != nil || !reflect.DeepEqual(got, memory) {
				t.Fatalf("Memory changed: %+v %v", got, err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenExisting(ctx, f.options)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := reopened.Close(); err != nil {
					t.Error(err)
				}
			}()
			assertIssueEditVersion(t, ctx, reopened, "beads/source", original)
			if got, err := reopened.ReadVersion(ctx, "links/first", f.links[0].Version); err != nil || !reflect.DeepEqual(got, f.links[0]) {
				t.Fatalf("reopened old Link: %+v %v", got, err)
			}
			if _, err := reopened.CurrentSnapshot(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDependencyUnlinkRefusalRollback(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			f := setupDependencyUnlink(t, backend, 1)
			s, ctx := f.store, f.ctx
			request := dependencyUnlinkRequest(f.links[0], f.source)
			state := workflowState(t, ctx, s)
			cases := []struct {
				name string
				edit func(*LinkDeleteRequest)
				want error
			}{
				{"missing-link-guard", func(r *LinkDeleteRequest) { r.ExpectedRevision = "" }, storage.ErrValidation},
				{"missing-source-guard", func(r *LinkDeleteRequest) { r.ExpectedSourceRevision = "" }, storage.ErrValidation},
				{"informational-default-is-not-dependency-guard", func(r *LinkDeleteRequest) {
					r.ExpectedSourceRevision = ""
					r.DefaultInformationalSource = true
				}, storage.ErrValidation},
				{"both-link-guards", func(r *LinkDeleteRequest) { r.Unconditional = true }, storage.ErrValidation},
				{"both-source-guards", func(r *LinkDeleteRequest) { r.UnconditionalSource = true }, storage.ErrValidation},
				{"stale-link", func(r *LinkDeleteRequest) { r.ExpectedRevision = "stale" }, ErrConflict},
				{"stale-source", func(r *LinkDeleteRequest) { r.ExpectedSourceRevision = "stale" }, ErrConflict},
				{"empty-actor", func(r *LinkDeleteRequest) { r.Actor = "" }, storage.ErrValidation},
				{"invalid-actor", func(r *LinkDeleteRequest) { r.Actor = string([]byte{255}) }, storage.ErrValidation},
				{"missing", func(r *LinkDeleteRequest) { r.Path = "links/missing" }, ErrNotFound},
				{"pair", func(r *LinkDeleteRequest) {
					r.Path = ""
					r.SourcePath = "beads/source"
					r.TargetPath = "beads/first"
					r.TypeURL = DependencyTypeURL(s.ScopeURL())
				}, ErrCapabilityUnavailable},
			}
			for _, tc := range cases {
				req := request
				tc.edit(&req)
				got, err := s.Unlink(ctx, req)
				if !errors.Is(err, tc.want) || !reflect.DeepEqual(got, LinkDeleteResult{}) {
					t.Fatalf("%s: %+v %v", tc.name, got, err)
				}
				if !reflect.DeepEqual(state, workflowState(t, ctx, s)) {
					t.Fatalf("%s changed state", tc.name)
				}
			}
			for _, stage := range []string{"coordination", "dependency-remove", "link-catalog", "link-retained", "issue-retained", "source-catalog", "source-retained"} {
				fault := errors.New("injected removal failure")
				s.afterWrite = func(at string) error {
					if at == stage {
						return fault
					}
					return nil
				}
				got, err := s.Unlink(ctx, request)
				s.afterWrite = nil
				if !errors.Is(err, fault) || !reflect.DeepEqual(got, LinkDeleteResult{}) {
					t.Fatalf("%s: %+v %v", stage, got, err)
				}
				if !reflect.DeepEqual(state, workflowState(t, ctx, s)) {
					t.Fatalf("%s leaked state", stage)
				}
			}
			canceled, cancel := context.WithCancel(ctx)
			s.afterWrite = func(stage string) error {
				if stage == "issue-retained" {
					cancel()
					return canceled.Err()
				}
				return nil
			}
			got, err := s.Unlink(canceled, request)
			s.afterWrite = nil
			cancel()
			if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, LinkDeleteResult{}) {
				t.Fatalf("cancellation: %+v %v", got, err)
			}
			if !reflect.DeepEqual(state, workflowState(t, ctx, s)) {
				t.Fatal("cancellation leaked state")
			}
			s.options.Binding.AuthorityID = "ffffffffffffffffffffffffffffffff"
			got, err = s.Unlink(ctx, request)
			s.options = f.options
			if !errors.Is(err, ErrInvalidStore) || !reflect.DeepEqual(got, LinkDeleteResult{}) {
				t.Fatalf("authority: %+v %v", got, err)
			}
			if !reflect.DeepEqual(state, workflowState(t, ctx, s)) {
				t.Fatal("authority refusal changed state")
			}
			if _, err := s.db.ExecContext(ctx, "INSERT INTO graph_preview_payloads(path,properties) VALUES('links/first','{}')"); err != nil {
				t.Fatal(err)
			}
			corrupt := workflowState(t, ctx, s)
			got, err = s.Unlink(ctx, request)
			if !errors.Is(err, ErrInvalidStore) || !reflect.DeepEqual(got, LinkDeleteResult{}) {
				t.Fatalf("duplicate payload: %+v %v", got, err)
			}
			if !reflect.DeepEqual(corrupt, workflowState(t, ctx, s)) {
				t.Fatal("unlink repaired duplicate payload")
			}
			if _, err := s.db.ExecContext(ctx, "DELETE FROM graph_preview_payloads WHERE path='links/first'"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.ExecContext(ctx, "DELETE FROM dependencies WHERE issue_id=?", f.source.Properties.ID); err != nil {
				t.Fatal(err)
			}
			corrupt = workflowState(t, ctx, s)
			got, err = s.Unlink(ctx, request)
			if !errors.Is(err, ErrInvalidStore) || !reflect.DeepEqual(got, LinkDeleteResult{}) {
				t.Fatalf("missing backing: %+v %v", got, err)
			}
			if !reflect.DeepEqual(corrupt, workflowState(t, ctx, s)) {
				t.Fatal("unlink repaired missing backing")
			}

		})
	}
}

func TestDependencyUnlinkDeletedIntegrity(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			f := setupDependencyUnlink(t, backend, 1)
			s, ctx := f.store, f.ctx
			removed, err := s.Unlink(ctx, dependencyUnlinkRequest(f.links[0], f.source))
			if err != nil {
				t.Fatal(err)
			}
			tombstoneRaw, err := canonicalJSON(removed.Link)
			if err != nil {
				t.Fatal(err)
			}
			priorRaw, err := canonicalJSON(f.links[0])
			if err != nil {
				t.Fatal(err)
			}
			// The missing-prior restore must put back the prior's own ordinal and
			// change_at. A fresh ordinal would sort the prior after the tombstone, and
			// workflowState is captured after each corruption, so nothing would notice.
			var priorOrdinal int64
			var priorChangeAt any
			if err := s.db.QueryRowContext(ctx, "SELECT ordinal, change_at FROM graph_preview_versions WHERE path='links/first' AND version=?", f.links[0].Version).Scan(&priorOrdinal, &priorChangeAt); err != nil {
				t.Fatal(err)
			}
			// These SQL mutations are corruption controls, never demonstration data.
			for _, bad := range []string{"retained-key", "missing-prior", "bad-prior", "bad-tombstone", "generic-payload"} {
				switch bad {
				case "retained-key":
					_, err = s.db.ExecContext(ctx, "UPDATE graph_preview_catalog SET backing_key='invalid-key' WHERE path='links/first'")
				case "missing-prior":
					_, err = s.db.ExecContext(ctx, "DELETE FROM graph_preview_versions WHERE path='links/first' AND version=?", f.links[0].Version)
				case "bad-prior":
					_, err = s.db.ExecContext(ctx, "UPDATE graph_preview_versions SET snapshot='{}' WHERE path='links/first' AND version=?", f.links[0].Version)
				case "bad-tombstone":
					_, err = s.db.ExecContext(ctx, "UPDATE graph_preview_versions SET snapshot='{}' WHERE path='links/first' AND version=?", removed.Link.Version)
				case "generic-payload":
					_, err = s.db.ExecContext(ctx, "INSERT INTO graph_preview_payloads(path,properties) VALUES('links/first','{}')")
				}
				if err != nil {
					t.Fatal(err)
				}
				state := workflowState(t, ctx, s)
				if _, err := s.ShowLink(ctx, "links/first"); !errors.Is(err, ErrInvalidStore) {
					t.Fatalf("%s masquerades as gone: %v", bad, err)
				}
				if bad == "retained-key" || bad == "generic-payload" {
					if _, err := s.CurrentSnapshot(ctx); !errors.Is(err, ErrInvalidStore) {
						t.Fatalf("%s inventory succeeded: %v", bad, err)
					}
				}
				if !reflect.DeepEqual(state, workflowState(t, ctx, s)) {
					t.Fatalf("%s reads repaired corrupt state", bad)
				}
				switch bad {
				case "retained-key":
					_, err = s.db.ExecContext(ctx, "UPDATE graph_preview_catalog SET backing_key=NULL WHERE path='links/first'")
				case "missing-prior":
					_, err = s.db.ExecContext(ctx, "INSERT INTO graph_preview_versions(path,version,snapshot,actor,ordinal,change_at) VALUES('links/first',?,?,?,?,?)", f.links[0].Version, priorRaw, f.links[0].Attribution.Actor, priorOrdinal, priorChangeAt)
				case "bad-prior":
					_, err = s.db.ExecContext(ctx, "UPDATE graph_preview_versions SET snapshot=? WHERE path='links/first' AND version=?", priorRaw, f.links[0].Version)
				case "bad-tombstone":
					_, err = s.db.ExecContext(ctx, "UPDATE graph_preview_versions SET snapshot=? WHERE path='links/first' AND version=?", tombstoneRaw, removed.Link.Version)
				case "generic-payload":
					_, err = s.db.ExecContext(ctx, "DELETE FROM graph_preview_payloads WHERE path='links/first'")
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.CurrentSnapshot(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDependencyUnlinkConcurrentWriters(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		for _, other := range []string{"remove", "add", "edit"} {
			t.Run(backend+"/"+other, func(t *testing.T) {
				f := setupDependencyUnlink(t, backend, 2)
				first, ctx := f.store, f.ctx
				ctx, cancel := context.WithCancel(ctx)
				defer cancel()
				var second *Store
				var err error
				if backend == "embedded" {
					second = &Store{db: first.db, options: f.options}
				} else {
					second, err = OpenExisting(ctx, f.options)
					if err != nil {
						t.Fatal(err)
					}
					defer func() {
						if err := second.Close(); err != nil {
							t.Error(err)
						}
					}()
				}
				// Embedded uses the production one-session pool. Only server forces
				// simultaneous SQL transactions at the final pre-commit stage.
				reached := make(chan struct{}, 2)
				release := make(chan struct{})
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
				type outcome struct {
					first bool
					err   error
				}
				results := make(chan outcome, 2)
				var writers sync.WaitGroup
				writers.Add(2)
				defer func() { cancel(); writers.Wait(); first.afterWrite = nil; second.afterWrite = nil }()
				go func() {
					defer writers.Done()
					_, err := first.Unlink(ctx, dependencyUnlinkRequest(f.links[0], f.source))
					results <- outcome{true, err}
				}()
				go func() {
					defer writers.Done()
					var err error
					switch other {
					case "remove":
						_, err = second.Unlink(ctx, dependencyUnlinkRequest(f.links[1], f.source))
					case "add":
						_, err = second.AddDependency(ctx, DependencyRequest{Path: "links/third", SourcePath: "beads/source", TargetPath: "beads/third", Actor: "racer", ExpectedSourceRevision: f.source.Revision})
					case "edit":
						_, err = second.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/source", Actor: "racer", ExpectedRevision: f.source.Revision, Title: issueEditString("Racing edit")})
					}
					results <- outcome{false, err}
				}()
				if backend == "server" {
					for range 2 {
						select {
						case <-reached:
						case result := <-results:
							t.Fatalf("writer before overlap: %v", result.err)
						case <-ctx.Done():
							t.Fatal(ctx.Err())
						}
					}
					close(release)
				}
				successes, conflicts := 0, 0
				firstWon := false
				for range 2 {
					select {
					case result := <-results:
						if result.err == nil {
							successes++
							firstWon = result.first
						} else if errors.Is(result.err, ErrConflict) && !errors.Is(result.err, ErrOutcomeUnknown) {
							conflicts++
						} else {
							t.Fatalf("writer error: %v", result.err)
						}
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				}
				writers.Wait()
				first.afterWrite = nil
				second.afterWrite = nil
				if successes != 1 || conflicts != 1 {
					t.Fatalf("success=%d conflict=%d", successes, conflicts)
				}
				current, err := first.ShowIssue(ctx, "beads/source")
				if err != nil {
					t.Fatal(err)
				}
				wantOwned := 2
				if firstWon || other == "remove" {
					wantOwned = 1
				} else if other == "add" {
					wantOwned = 3
				}
				if len(current.Owned) != wantOwned {
					t.Fatalf("wrong winner owned count: %d want%d", len(current.Owned), wantOwned)
				}
				if (current.Properties.Title == "Racing edit") != (!firstWon && other == "edit") {
					t.Fatal("losing edit leaked or winning edit lost")
				}
				if firstWon {
					if _, err := first.ShowLink(ctx, "links/first"); !errors.Is(err, ErrGone) {
						t.Fatalf("remove winner absent: %v", err)
					}
				} else if got, err := first.ShowLink(ctx, "links/first"); err != nil || !reflect.DeepEqual(got, f.links[0]) {
					t.Fatalf("losing removal leaked: %+v %v", got, err)
				}
				if _, err := first.CurrentSnapshot(ctx); err != nil {
					t.Fatal(err)
				}
				assertDependencyReadiness(t, ctx, first, current, false)
				assertIssueEditCounts(t, ctx, first, current.Properties.ID, 4)
				assertIssueEditVersion(t, ctx, first, "beads/source", f.source)
				assertIssueEditVersion(t, ctx, first, "beads/source", current)
				for i, path := range []string{"beads/first", "beads/second", "beads/third"} {
					if got, err := first.ShowIssue(ctx, path); err != nil || !reflect.DeepEqual(got, f.targets[i]) {
						t.Fatalf("target changed: %+v %v", got, err)
					}
				}
			})
		}
	}
}
