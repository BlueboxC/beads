//go:build cgo

package graphstore

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

func issueEditString(value string) *string { return &value }

func assertIssueEditVersion(t *testing.T, ctx context.Context, s *Store, path string, want IssueRecord) {
	t.Helper()
	// Current row locks and content hashes are deliberately absent from retained JSON.
	properties := *want.Properties
	properties.ContentHash, properties.RowVersion = "", 0
	want.Properties = &properties
	got, err := s.ReadVersion(ctx, path, want.Version)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("retained %s: got=%+v want=%+v err=%v", want.Version, got, want, err)
	}
}

func assertIssueEditCounts(t *testing.T, ctx context.Context, s *Store, id string, want int) {
	t.Helper()
	for _, table := range []string{"issue_versions", "graph_preview_issue_versions"} {
		var count int
		if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE issue_id=?", id).Scan(&count); err != nil || count != want {
			t.Fatalf("%s snapshots=%d want%d: %v", table, count, want, err)
		}
	}
}

func TestIssuePropertyEmptyMergeAndEstimateClear(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, options := issueExperimentOptions(t, backend)
			store, err := OpenExisting(ctx, options)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := store.Close(); err != nil {
					t.Error(err)
				}
			}()
			original, err := store.CreateIssue(ctx, "beads/work", plainIssue("Work"))
			if err != nil {
				t.Fatal(err)
			}
			empty, err := store.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "editor", ExpectedRevision: original.Revision, PropertiesProvided: true})
			if err != nil || empty.Changed || empty.Issue.Revision != original.Revision {
				t.Fatalf("empty merge: %+v %v", empty, err)
			}
			assertIssueEditCounts(t, ctx, store, original.Properties.ID, 1)
			minutes := 7
			set, err := store.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "editor", ExpectedRevision: original.Revision, EstimatedMinutes: &minutes})
			if err != nil || !set.Changed || set.Issue.Properties.EstimatedMinutes == nil {
				t.Fatalf("set estimate: %+v %v", set, err)
			}
			cleared, err := store.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "editor", ExpectedRevision: set.Issue.Revision, ClearEstimatedMinutes: true})
			if err != nil || !cleared.Changed || cleared.Issue.Properties.EstimatedMinutes != nil {
				t.Fatalf("clear estimate: %+v %v", cleared, err)
			}
			assertIssueEditCounts(t, ctx, store, original.Properties.ID, 3)
			noop, err := store.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "another", ExpectedRevision: cleared.Issue.Revision, ClearEstimatedMinutes: true})
			if err != nil || noop.Changed || noop.Issue.Revision != cleared.Issue.Revision {
				t.Fatalf("repeat clear: %+v %v", noop, err)
			}
			assertIssueEditCounts(t, ctx, store, original.Properties.ID, 3)
		})
	}
}

func TestIssueUpdateLifecycle(t *testing.T) {
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
			original, err := s.CreateIssue(ctx, "beads/work", plainIssue("Work"))
			if err != nil {
				t.Fatal(err)
			}
			target, err := s.CreateIssue(ctx, "beads/prereq", plainIssue("Prerequisite"))
			if err != nil {
				t.Fatal(err)
			}
			dep, err := s.AddDependency(ctx, DependencyRequest{Path: "links/block", SourcePath: "beads/work", TargetPath: "beads/prereq", Actor: "author"})
			if err != nil {
				t.Fatal(err)
			}
			memory, err := s.Create(ctx, CreateRequest{Path: "beads/context", Title: "Context", Body: "Context"})
			if err != nil {
				t.Fatal(err)
			}
			info, err := s.AddInformationalLink(ctx, LinkCreateRequest{Path: "links/context", SourcePath: "beads/work", TargetPath: "beads/context", Actor: "author"})
			if err != nil {
				t.Fatal(err)
			}
			readyBefore, err := s.ReadyIssues(ctx)
			if err != nil {
				t.Fatal(err)
			}
			request := UpdateIssueRequest{Path: "beads/work", Actor: "editor", ExpectedRevision: dep.Source.Revision,
				Title: issueEditString("Revised 雪"), Description: issueEditString("  revised\r\nbody 😀  "),
				Design: issueEditString("design"), AcceptanceCriteria: issueEditString("acceptance")}
			edited, err := s.UpdateIssue(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			p := edited.Issue.Properties
			if !edited.Changed || edited.Issue.Revision == dep.Source.Revision || edited.Issue.Version != edited.Issue.Revision || edited.Issue.ID != original.ID || edited.Issue.Type != original.Type || edited.Issue.Attribution.Actor != "editor" || edited.Issue.Attribution.Status != "claimed" || p.Title != *request.Title || p.Description != *request.Description || p.Notes != original.Properties.Notes || p.Design != *request.Design || p.AcceptanceCriteria != *request.AcceptanceCriteria || !reflect.DeepEqual(edited.Issue.Owned, dep.Source.Owned) {
				t.Fatalf("wrong edit: %+v properties=%+v", edited, p)
			}
			if p.Status != original.Properties.Status || p.Priority != original.Properties.Priority || p.IssueType != original.Properties.IssueType || !reflect.DeepEqual(p.Labels, original.Properties.Labels) || !reflect.DeepEqual(p.ClosedAt, original.Properties.ClosedAt) {
				t.Fatal("text edit changed unrelated Issue fields")
			}
			assertIssueEditCounts(t, ctx, s, p.ID, 3)
			for path, want := range map[string]LinkRecord{"links/block": dep.Link, "links/context": info.Link} {
				if got, err := s.ShowLink(ctx, path); err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("Link changed: %+v %v", got, err)
				}
			}
			if got, err := s.ShowIssue(ctx, "beads/prereq"); err != nil || !reflect.DeepEqual(got, target) {
				t.Fatalf("target changed: %+v %v", got, err)
			}
			if got, err := s.Show(ctx, "beads/context"); err != nil || !reflect.DeepEqual(got, memory) {
				t.Fatalf("Memory changed: %+v %v", got, err)
			}
			if got, err := s.ReadyIssues(ctx); err != nil || !reflect.DeepEqual(got, readyBefore) {
				t.Fatalf("readiness changed: %+v %v", got, err)
			}
			state := workflowState(t, ctx, s)
			if _, err := s.UpdateIssue(ctx, request); !errors.Is(err, ErrConflict) {
				t.Fatalf("stale no-op: %v", err)
			}
			request.ExpectedRevision, request.Actor = edited.Issue.Revision, "different actor"
			noop, err := s.UpdateIssue(ctx, request)
			if err != nil || noop.Changed || !reflect.DeepEqual(noop.Issue, edited.Issue) {
				t.Fatalf("no-op: %+v %v", noop, err)
			}
			if !reflect.DeepEqual(state, workflowState(t, ctx, s)) {
				t.Fatal("no-op/stale guard changed persisted state")
			}
			cleared, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "clearer", Unconditional: true, Description: issueEditString(""), Design: issueEditString(""), AcceptanceCriteria: issueEditString("")})
			if err != nil || !cleared.Changed {
				t.Fatalf("clear: %+v %v", cleared, err)
			}
			cp := cleared.Issue.Properties
			if cp.Title != p.Title || cp.Description != "" || cp.Notes != p.Notes || cp.Design != "" || cp.AcceptanceCriteria != "" || !reflect.DeepEqual(cleared.Issue.Owned, edited.Issue.Owned) {
				t.Fatalf("clear/omission: %+v", cleared)
			}
			closed, err := s.CloseIssue(ctx, "beads/prereq", "done", "closer")
			if err != nil {
				t.Fatal(err)
			}
			closedEdit, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/prereq", Actor: "editor", ExpectedRevision: closed.Issue.Revision, Design: issueEditString("post-close design")})
			if err != nil || !closedEdit.Changed || closedEdit.Issue.Properties.Status != types.StatusClosed || !reflect.DeepEqual(closedEdit.Issue.Properties.ClosedAt, closed.Issue.Properties.ClosedAt) || closedEdit.Issue.Properties.CloseReason != closed.Issue.Properties.CloseReason {
				t.Fatalf("closed text edit: %+v %v", closedEdit, err)
			}
			assertIssueEditCounts(t, ctx, s, target.Properties.ID, 3)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = OpenExisting(ctx, o)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := s.ShowIssue(ctx, "beads/work"); err != nil || !reflect.DeepEqual(got, cleared.Issue) {
				t.Fatalf("reopen: %+v %v", got, err)
			}
			for _, want := range []IssueRecord{original, dep.Source, edited.Issue, cleared.Issue} {
				assertIssueEditVersion(t, ctx, s, "beads/work", want)
			}
			for _, want := range []IssueRecord{target, closed.Issue, closedEdit.Issue} {
				assertIssueEditVersion(t, ctx, s, "beads/prereq", want)
			}
			assertIssueEditCounts(t, ctx, s, p.ID, 4)
		})
	}
}

func TestIssueUpdateRefusalAndRollback(t *testing.T) {
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
			issue, err := s.CreateIssue(ctx, "beads/work", plainIssue("Before"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Create(ctx, CreateRequest{Path: "beads/memory", Body: "body"}); err != nil {
				t.Fatal(err)
			}
			req := UpdateIssueRequest{Path: "beads/work", Actor: "editor", ExpectedRevision: issue.Revision, Title: issueEditString("After")}
			state := workflowState(t, ctx, s)
			bads := []struct {
				name string
				edit func(*UpdateIssueRequest)
				want error
			}{
				{"missing-guard", func(r *UpdateIssueRequest) { r.ExpectedRevision = "" }, storage.ErrValidation},
				{"both-guards", func(r *UpdateIssueRequest) { r.Unconditional = true }, storage.ErrValidation},
				{"stale", func(r *UpdateIssueRequest) { r.ExpectedRevision = "stale" }, ErrConflict},
				{"missing", func(r *UpdateIssueRequest) { r.Path = "beads/missing" }, ErrNotFound},
				{"memory", func(r *UpdateIssueRequest) { r.Path = "beads/memory" }, ErrCapabilityUnavailable},
				{"link-path", func(r *UpdateIssueRequest) { r.Path = "links/wrong" }, storage.ErrValidation},
				{"empty-actor", func(r *UpdateIssueRequest) { r.Actor = "" }, storage.ErrValidation},
				{"actor-utf8", func(r *UpdateIssueRequest) { r.Actor = string([]byte{255}) }, storage.ErrValidation},
				{"no-fields", func(r *UpdateIssueRequest) { r.Title = nil }, storage.ErrValidation},
				{"empty-title", func(r *UpdateIssueRequest) { r.Title = issueEditString("") }, storage.ErrValidation},
				{"long-title", func(r *UpdateIssueRequest) { r.Title = issueEditString(strings.Repeat("a", 501)) }, storage.ErrValidation},
			}
			for _, name := range []string{"title", "description", "design", "acceptance"} {
				bads = append(bads, struct {
					name string
					edit func(*UpdateIssueRequest)
					want error
				}{name + "-utf8", func(r *UpdateIssueRequest) {
					bad := issueEditString(string([]byte{255}))
					switch name {
					case "title":
						r.Title = bad
					case "description":
						r.Description = bad
					case "design":
						r.Design = bad
					case "acceptance":
						r.AcceptanceCriteria = bad
					}
				}, storage.ErrValidation})
			}
			for _, tc := range bads {
				r := req
				tc.edit(&r)
				got, err := s.UpdateIssue(ctx, r)
				if !errors.Is(err, tc.want) || !reflect.DeepEqual(got, IssueMutationResult{}) {
					t.Fatalf("%s: %+v %v", tc.name, got, err)
				}
			}
			for _, stage := range []string{"coordination", "issue-update", "issue-retained", "source-catalog", "source-retained"} {
				fault := errors.New("injected Issue edit failure")
				s.afterWrite = func(at string) error {
					if at == stage {
						return fault
					}
					return nil
				}
				got, err := s.UpdateIssue(ctx, req)
				s.afterWrite = nil
				if !errors.Is(err, fault) || !reflect.DeepEqual(got, IssueMutationResult{}) {
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
			got, err := s.UpdateIssue(canceled, req)
			cancel()
			s.afterWrite = nil
			if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, IssueMutationResult{}) {
				t.Fatalf("cancellation: %+v %v", got, err)
			}
			s.options.Binding.AuthorityID = "ffffffffffffffffffffffffffffffff"
			got, err = s.UpdateIssue(ctx, req)
			s.options = o
			if !errors.Is(err, ErrInvalidStore) || !reflect.DeepEqual(got, IssueMutationResult{}) {
				t.Fatalf("authority: %+v %v", got, err)
			}
			if !reflect.DeepEqual(state, workflowState(t, ctx, s)) {
				t.Fatal("refusal/cancellation changed persisted state")
			}
			if _, err := s.db.ExecContext(ctx, `DELETE FROM graph_preview_issue_versions WHERE path=? AND version=?`, req.Path, issue.Version); err != nil {
				t.Fatal(err)
			}
			corrupt := workflowState(t, ctx, s)
			got, err = s.UpdateIssue(ctx, req)
			if !errors.Is(err, ErrInvalidStore) || !reflect.DeepEqual(got, IssueMutationResult{}) {
				t.Fatalf("missing retained mapping: %+v %v", got, err)
			}
			if !reflect.DeepEqual(corrupt, workflowState(t, ctx, s)) {
				t.Fatal("edit repaired corrupt store")
			}
		})
	}
}

func TestIssueUpdateConcurrentWriters(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		for _, other := range []string{"text", "dependency"} {
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
				source, err := first.CreateIssue(ctx, "beads/work", plainIssue("Original"))
				if err != nil {
					t.Fatal(err)
				}
				target, err := first.CreateIssue(ctx, "beads/prereq", plainIssue("Prerequisite"))
				if err != nil {
					t.Fatal(err)
				}
				var second *Store
				if backend == "embedded" {
					// Production one-session pool: concurrent callers serialize; only the
					// ordinary server below claims overlapping SQL transactions.
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
				defer func() { cancel(); writers.Wait() }()
				go func() {
					defer writers.Done()
					_, err := first.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "one", ExpectedRevision: source.Revision, Title: issueEditString("Writer one")})
					results <- err
				}()
				go func() {
					defer writers.Done()
					var err error
					if other == "text" {
						_, err = second.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "two", ExpectedRevision: source.Revision, Title: issueEditString("Writer two")})
					} else {
						_, err = second.AddDependency(ctx, DependencyRequest{Path: "links/block", SourcePath: "beads/work", TargetPath: "beads/prereq", Actor: "two", ExpectedSourceRevision: source.Revision})
					}
					results <- err
				}()
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
				writers.Wait()
				first.afterWrite, second.afterWrite = nil, nil
				if successes != 1 || conflicts != 1 {
					t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
				}
				current, err := first.ShowIssue(ctx, "beads/work")
				if err != nil {
					t.Fatal(err)
				}
				if other == "text" {
					if current.Properties.Title != "Writer one" && current.Properties.Title != "Writer two" {
						t.Fatalf("lost edit: %+v", current.Properties)
					}
					if len(current.Owned) != 0 {
						t.Fatal("text race added Links")
					}
				} else if current.Properties.Title == "Writer one" {
					if len(current.Owned) != 0 {
						t.Fatal("both effects committed")
					}
					if _, err := first.ShowLink(ctx, "links/block"); !errors.Is(err, ErrNotFound) {
						t.Fatalf("losing Dependency survived: %v", err)
					}
				} else {
					if current.Properties.Title != "Original" || len(current.Owned) != 1 {
						t.Fatalf("incomplete Dependency winner: %+v", current)
					}
					if _, err := first.ShowLink(ctx, "links/block"); err != nil {
						t.Fatal(err)
					}
				}
				if got, err := first.ShowIssue(ctx, "beads/prereq"); err != nil || !reflect.DeepEqual(got, target) {
					t.Fatalf("target changed: %+v %v", got, err)
				}
				assertIssueEditVersion(t, ctx, first, "beads/work", source)
				assertIssueEditVersion(t, ctx, first, "beads/work", current)
				assertIssueEditCounts(t, ctx, first, source.Properties.ID, 2)
			})
		}
	}
}
