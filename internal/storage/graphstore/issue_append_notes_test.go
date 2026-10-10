//go:build cgo

package graphstore

import (
	"context"
	"errors"
	"net"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
)

func assertIssueAppendTransition(t *testing.T, before, after IssueRecord, notes, actor string) {
	t.Helper()
	if after.Properties == nil || after.Properties.Notes != notes || after.ID != before.ID || after.Type != before.Type || after.Revision == before.Revision || after.Version != after.Revision || after.Attribution.Actor != actor || !reflect.DeepEqual(after.Owned, before.Owned) {
		t.Fatalf("incomplete append transition: %+v properties=%+v", after, after.Properties)
	}
	p := *after.Properties
	p.Notes, p.UpdatedAt = before.Properties.Notes, before.Properties.UpdatedAt
	p.ContentHash, p.RowVersion = before.Properties.ContentHash, before.Properties.RowVersion
	if !reflect.DeepEqual(p, *before.Properties) {
		t.Fatal("append changed unrelated Issue properties")
	}
}

func TestIssueAppendNotesLifecycle(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s, original, target, dependency := reopenFixture(t, backend)
			memory, err := s.Create(ctx, CreateRequest{Path: "beads/context", Body: "unchanged Memory"})
			if err != nil {
				t.Fatal(err)
			}
			beforeState := reopenState(t, ctx, s)
			s.afterWrite = func(stage string) error { return errors.New("empty no-op reached " + stage) }
			noop, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "empty", ExpectedRevision: original.Revision, AppendNotes: issueEditString("")})
			s.afterWrite = nil
			if err != nil || noop.Changed || !reflect.DeepEqual(noop.Issue, original) || !reflect.DeepEqual(beforeState, reopenState(t, ctx, s)) {
				t.Fatalf("empty-on-empty no-op: %+v %v", noop, err)
			}
			current, count := original, 2
			for _, text := range []string{"First line — 雪\r\n  ", "-", "", "\nlast\r\n"} {
				want := text
				if current.Properties.Notes != "" {
					want = current.Properties.Notes + "\n" + text
				}
				got, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "append-author", ExpectedRevision: current.Revision, AppendNotes: issueEditString(text)})
				if err != nil || !got.Changed {
					t.Fatalf("append %q: %+v %v", text, got, err)
				}
				assertIssueAppendTransition(t, current, got.Issue, want, "append-author")
				assertIssueEditVersion(t, ctx, s, "beads/work", current)
				assertIssueEditVersion(t, ctx, s, "beads/work", got.Issue)
				count++
				assertIssueEditCounts(t, ctx, s, original.Properties.ID, count)
				current = got.Issue
			}
			// Caller-owned pointers are admitted once, before coordination.
			text := "captured caller text"
			s.afterWrite = func(stage string) error {
				if stage == "coordination" {
					text = "later caller mutation"
				}
				return nil
			}
			captured, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "capture", ExpectedRevision: current.Revision, AppendNotes: &text})
			s.afterWrite = nil
			if err != nil || !captured.Changed {
				t.Fatalf("captured append: %+v %v", captured, err)
			}
			assertIssueAppendTransition(t, current, captured.Issue, current.Properties.Notes+"\ncaptured caller text", "capture")
			assertIssueEditVersion(t, ctx, s, "beads/work", current)
			current, count = captured.Issue, count+1
			assertIssueEditCounts(t, ctx, s, original.Properties.ID, count)
			// Text and priority share this one mutation and retained version.
			mixed, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "mixed", ExpectedRevision: current.Revision, AppendNotes: issueEditString("mixed edit"), Title: issueEditString("Revised title"), Priority: issuePriority(0)})
			if err != nil || !mixed.Changed || mixed.Issue.Properties.Title != "Revised title" || mixed.Issue.Properties.Priority != 0 {
				t.Fatalf("mixed append: %+v %v", mixed, err)
			}
			comparison := current
			properties := *current.Properties
			properties.Title, properties.Priority = "Revised title", 0
			comparison.Properties = &properties
			assertIssueAppendTransition(t, comparison, mixed.Issue, current.Properties.Notes+"\nmixed edit", "mixed")
			assertIssueEditVersion(t, ctx, s, "beads/work", current)
			current, count = mixed.Issue, count+1
			assertIssueEditCounts(t, ctx, s, original.Properties.ID, count)
			// Omitting append preserves the complete existing notes bytes.
			omitted, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "scalar", ExpectedRevision: current.Revision, Title: issueEditString("Only title")})
			if err != nil || !omitted.Changed || omitted.Issue.Properties.Notes != current.Properties.Notes {
				t.Fatalf("omitted append changed notes: %+v %v", omitted, err)
			}
			assertIssueEditVersion(t, ctx, s, "beads/work", current)
			current, count = omitted.Issue, count+1
			assertIssueEditCounts(t, ctx, s, original.Properties.ID, count)
			if got, err := s.ShowIssue(ctx, "beads/work"); err != nil || !reflect.DeepEqual(got, current) {
				t.Fatalf("current: %+v %v", got, err)
			}
			assertIssueEditVersion(t, ctx, s, "beads/work", current)
			if got, err := s.ShowIssue(ctx, "beads/prereq"); err != nil || !reflect.DeepEqual(got, target) {
				t.Fatalf("target changed: %+v %v", got, err)
			}
			if got, err := s.ShowLink(ctx, "links/block"); err != nil || !reflect.DeepEqual(got, dependency) {
				t.Fatalf("owned Link changed: %+v %v", got, err)
			}
			if got, err := s.Read(ctx, "beads/context"); err != nil || !reflect.DeepEqual(got, memory) {
				t.Fatalf("Memory changed: %+v %v", got, err)
			}
		})
	}
}

func TestIssueAppendNotesClaimedAndClosed(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s, original, _, _ := reopenFixture(t, backend)
			claimed, err := s.ClaimIssue(ctx, "beads/work", "holder")
			if err != nil || !claimed.Changed {
				t.Fatalf("claim: %+v %v", claimed, err)
			}
			appended, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "holder", ExpectedRevision: claimed.Issue.Revision, AppendNotes: issueEditString("Work in progress")})
			if err != nil || !appended.Changed {
				t.Fatalf("claimed append: %+v %v", appended, err)
			}
			assertIssueAppendTransition(t, claimed.Issue, appended.Issue, "Work in progress", "holder")
			assertClaimLease(t, ctx, s, original.Properties.ID, "holder")
			assertIssueEditVersion(t, ctx, s, "beads/work", claimed.Issue)
			assertIssueEditCounts(t, ctx, s, original.Properties.ID, 4)
			if _, err := s.CloseIssue(ctx, "beads/prereq", "done", "reviewer"); err != nil {
				t.Fatal(err)
			}
			closed, err := s.CloseIssue(ctx, "beads/work", "Delivered", "holder")
			if err != nil || !closed.Changed {
				t.Fatalf("close: %+v %v", closed, err)
			}
			after, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "holder", ExpectedRevision: closed.Issue.Revision, AppendNotes: issueEditString("Post-completion context")})
			if err != nil || !after.Changed {
				t.Fatalf("closed append: %+v %v", after, err)
			}
			assertIssueAppendTransition(t, closed.Issue, after.Issue, "Work in progress\nPost-completion context", "holder")
			assertIssueEditCounts(t, ctx, s, original.Properties.ID, 6)
			assertIssueEditVersion(t, ctx, s, "beads/work", closed.Issue)
			assertIssueEditVersion(t, ctx, s, "beads/work", after.Issue)
			assertAssigneeLeaseCount(t, ctx, s, original.Properties.ID, 0)
			assertReadyIDs(t, ctx, s)
		})
	}
}

func TestIssueAppendNotesRefusalAndRollback(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, options, s, original, _, _ := reopenFixture(t, backend)
			if _, err := s.Create(ctx, CreateRequest{Path: "beads/context", Body: "Memory"}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.CreateIssue(ctx, "beads/extra", plainIssue("Other prerequisite")); err != nil {
				t.Fatal(err)
			}
			linked, err := s.AddDependency(ctx, DependencyRequest{Path: "links/extra", SourcePath: "beads/work", TargetPath: "beads/extra", Actor: "link-author"})
			if err != nil {
				t.Fatal(err)
			}
			current := linked.Source
			state := reopenState(t, ctx, s)
			for _, tc := range []struct {
				name    string
				request UpdateIssueRequest
				want    error
			}{
				{"missing-guard", UpdateIssueRequest{Path: "beads/work", Actor: "a", AppendNotes: issueEditString("x")}, storage.ErrValidation},
				{"both-guards", UpdateIssueRequest{Path: "beads/work", Actor: "a", ExpectedRevision: current.Revision, Unconditional: true, AppendNotes: issueEditString("x")}, storage.ErrValidation},
				{"stale-owned-empty-noop", UpdateIssueRequest{Path: "beads/work", Actor: "a", ExpectedRevision: original.Revision, AppendNotes: issueEditString("")}, ErrConflict},
				{"invalid-utf8", UpdateIssueRequest{Path: "beads/work", Actor: "a", ExpectedRevision: current.Revision, AppendNotes: issueEditString("\xff")}, storage.ErrValidation},
				{"missing", UpdateIssueRequest{Path: "beads/missing", Actor: "a", Unconditional: true, AppendNotes: issueEditString("x")}, ErrNotFound},
				{"Memory", UpdateIssueRequest{Path: "beads/context", Actor: "a", Unconditional: true, AppendNotes: issueEditString("x")}, ErrCapabilityUnavailable},
			} {
				t.Run(tc.name, func(t *testing.T) {
					got, err := s.UpdateIssue(ctx, tc.request)
					if !errors.Is(err, tc.want) || !reflect.DeepEqual(got, IssueMutationResult{}) || !reflect.DeepEqual(state, reopenState(t, ctx, s)) {
						t.Fatalf("refusal: %+v %v", got, err)
					}
				})
			}
			for _, stage := range []string{"coordination", "issue-update", "issue-retained", "source-catalog", "source-retained"} {
				t.Run(stage, func(t *testing.T) {
					fault := errors.New("append rollback")
					s.afterWrite = func(at string) error {
						if at == stage {
							return fault
						}
						return nil
					}
					got, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "fault", ExpectedRevision: current.Revision, AppendNotes: issueEditString("must rollback"), Title: issueEditString("also rollback")})
					s.afterWrite = nil
					if !errors.Is(err, fault) || !reflect.DeepEqual(got, IssueMutationResult{}) || !reflect.DeepEqual(state, reopenState(t, ctx, s)) {
						t.Fatalf("rollback: %+v %v", got, err)
					}
				})
			}
			canceled, cancel := context.WithCancel(ctx)
			s.afterWrite = func(stage string) error {
				if stage == "source-retained" {
					cancel()
					return canceled.Err()
				}
				return nil
			}
			got, err := s.UpdateIssue(canceled, UpdateIssueRequest{Path: "beads/work", Actor: "cancel", ExpectedRevision: current.Revision, AppendNotes: issueEditString("canceled")})
			cancel()
			s.afterWrite = nil
			if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, IssueMutationResult{}) || !reflect.DeepEqual(state, reopenState(t, ctx, s)) {
				t.Fatalf("cancellation: %+v %v", got, err)
			}
			s.options.Binding.AuthorityID = "ffffffffffffffffffffffffffffffff"
			got, err = s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "authority", Unconditional: true, AppendNotes: issueEditString("refused")})
			s.options = options
			if !errors.Is(err, ErrInvalidStore) || !reflect.DeepEqual(got, IssueMutationResult{}) || !reflect.DeepEqual(state, reopenState(t, ctx, s)) {
				t.Fatalf("authority: %+v %v", got, err)
			}
			assertIssueEditVersion(t, ctx, s, "beads/work", current)
		})
	}
}

func TestIssueAppendNotesConcurrentWriters(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			for _, guarded := range []bool{true, false} {
				name := "unconditional"
				if guarded {
					name = "same-revision"
				}
				t.Run(name, func(t *testing.T) {
					base, options, first, source, target, dependency := reopenFixture(t, backend)
					ctx, cancel := context.WithTimeout(base, time.Minute)
					defer cancel()
					seed, err := first.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "seed", ExpectedRevision: source.Revision, AppendNotes: issueEditString("seed")})
					if err != nil || !seed.Changed {
						t.Fatalf("seed: %+v %v", seed, err)
					}
					source = seed.Issue
					second := &Store{db: first.db, options: options}
					if backend == "server" {
						second, err = OpenExisting(ctx, options)
						if err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() {
							if err := second.Close(); err != nil {
								t.Error(err)
							}
						})
					}
					var beforeEvents int
					if err := first.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE issue_id=?", source.Properties.ID).Scan(&beforeEvents); err != nil {
						t.Fatal(err)
					}
					reached, release := make(chan struct{}, 2), make(chan struct{})
					pause := func(stage string) error {
						// Force independent server snapshots; embedded keeps production
						// pool1 and concurrent callers serialize, with no fake overlap.
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
						actor string
						value IssueMutationResult
						err   error
					}
					results := make(chan outcome, 2)
					var writers sync.WaitGroup
					writers.Add(2)
					defer func() { cancel(); writers.Wait(); first.afterWrite = nil; second.afterWrite = nil }()
					for i, s := range []*Store{first, second} {
						actor := []string{"line-a", "line-b"}[i]
						go func() {
							defer writers.Done()
							request := UpdateIssueRequest{Path: "beads/work", Actor: actor, AppendNotes: issueEditString(actor), Unconditional: !guarded}
							if guarded {
								request.ExpectedRevision = source.Revision
							}
							value, err := s.UpdateIssue(ctx, request)
							results <- outcome{actor, value, err}
						}()
					}
					if backend == "server" {
						for range 2 {
							select {
							case <-reached:
							case early := <-results:
								t.Fatalf("before forced overlap: %+v", early)
							case <-ctx.Done():
								t.Fatal(ctx.Err())
							}
						}
						close(release)
					}
					accepted := map[string]IssueRecord{}
					conflicts := 0
					for range 2 {
						select {
						case result := <-results:
							if result.err == nil {
								if !result.value.Changed {
									t.Fatalf("append was no-op: %+v", result)
								}
								accepted[result.actor] = result.value.Issue
							} else if errors.Is(result.err, ErrConflict) && !errors.Is(result.err, ErrOutcomeUnknown) && reflect.DeepEqual(result.value, IssueMutationResult{}) {
								conflicts++
							} else {
								t.Fatalf("bad append outcome: %+v", result)
							}
						case <-ctx.Done():
							t.Fatal(ctx.Err())
						}
					}
					writers.Wait()
					first.afterWrite = nil
					second.afterWrite = nil
					wantSuccesses := 1
					if backend == "embedded" && !guarded {
						wantSuccesses = 2
					}
					if len(accepted) != wantSuccesses || conflicts != 2-wantSuccesses {
						t.Fatalf("accepted%d conflicts%d", len(accepted), conflicts)
					}
					current, err := first.ShowIssue(ctx, "beads/work")
					if err != nil {
						t.Fatal(err)
					}
					if len(accepted) == 2 {
						if current.Properties.Notes != "seed\nline-a\nline-b" && current.Properties.Notes != "seed\nline-b\nline-a" {
							t.Fatalf("lost/duplicated successful append: %q", current.Properties.Notes)
						}
					} else {
						for actor, record := range accepted {
							if current.Properties.Notes != "seed\n"+actor || !reflect.DeepEqual(current, record) {
								t.Fatalf("not complete winner: %+v", current)
							}
						}
					}
					assertIssueAppendTransition(t, source, current, current.Properties.Notes, current.Attribution.Actor)
					for _, actor := range []string{"line-a", "line-b"} {
						wantEvents := 0
						if record, ok := accepted[actor]; ok {
							wantEvents = 1
							other := "line-a"
							if actor == other {
								other = "line-b"
							}
							if record.Properties.Notes != "seed\n"+actor && !(len(accepted) == 2 && record.Properties.Notes == "seed\n"+other+"\n"+actor) {
								t.Fatalf("accepted append used wrong predecessor: %q", record.Properties.Notes)
							}
							assertIssueAppendTransition(t, source, record, record.Properties.Notes, actor)
							assertIssueEditVersion(t, ctx, first, "beads/work", record)
						}
						var events int
						if err := first.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE issue_id=? AND actor=?", source.Properties.ID, actor).Scan(&events); err != nil || events != wantEvents {
							t.Fatalf("actor%s audit%d want%d: %v", actor, events, wantEvents, err)
						}
					}
					var afterEvents int
					if err := first.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE issue_id=?", source.Properties.ID).Scan(&afterEvents); err != nil || afterEvents != beforeEvents+len(accepted) {
						t.Fatalf("audit count%d before%d: %v", afterEvents, beforeEvents, err)
					}
					assertIssueEditCounts(t, ctx, first, source.Properties.ID, 3+len(accepted))
					assertIssueEditVersion(t, ctx, first, "beads/work", source)
					if got, err := first.ShowIssue(ctx, "beads/prereq"); err != nil || !reflect.DeepEqual(got, target) {
						t.Fatalf("target changed: %+v %v", got, err)
					}
					if got, err := first.ShowLink(ctx, "links/block"); err != nil || !reflect.DeepEqual(got, dependency) {
						t.Fatalf("owned Link changed: %+v %v", got, err)
					}
				})
			}
		})
	}
}

func TestIssueAppendNotesLostCommitResponse(t *testing.T) {
	ctx, options, direct, source, _, _ := reopenFixture(t, "server")
	proxyPort, observed := startCommitLossProxy(t, net.JoinHostPort(options.ServerHost, strconv.Itoa(options.ServerPort)))
	proxied := options
	proxied.ServerPort = proxyPort
	s, err := OpenExisting(ctx, proxied)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	got, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "lost-append", ExpectedRevision: source.Revision, AppendNotes: issueEditString("exactly once — 雪")})
	if !errors.Is(err, ErrOutcomeUnknown) || errors.Is(err, ErrConflict) || !reflect.DeepEqual(got, IssueMutationResult{}) {
		t.Fatalf("lost COMMIT misclassified: %+v %v", got, err)
	}
	select {
	case packet := <-observed:
		if len(packet) == 0 || packet[0] != 0 {
			t.Fatalf("no COMMIT success witness: %x", packet)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// Inspect independently; do not replay an append after uncertain COMMIT.
	current, err := direct.ShowIssue(ctx, "beads/work")
	if err != nil {
		t.Fatal(err)
	}
	assertIssueAppendTransition(t, source, current, "exactly once — 雪", "lost-append")
	assertIssueEditCounts(t, ctx, direct, source.Properties.ID, 3)
	assertIssueEditVersion(t, ctx, direct, "beads/work", source)
	assertIssueEditVersion(t, ctx, direct, "beads/work", current)
	var events int
	if err := direct.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE issue_id=? AND actor='lost-append'", source.Properties.ID).Scan(&events); err != nil || events != 1 {
		t.Fatalf("append replayed or lost event: %d %v", events, err)
	}
}
