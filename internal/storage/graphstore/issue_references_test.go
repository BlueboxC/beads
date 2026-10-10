//go:build cgo

package graphstore

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

func assertIssueReferencesTransition(t *testing.T, before, after IssueRecord, external *string, spec, title, actor string) {
	t.Helper()
	if after.Properties == nil || !reflect.DeepEqual(after.Properties.ExternalRef, external) || after.Properties.SpecID != spec || after.Properties.Title != title || after.Revision == before.Revision || after.Version != after.Revision || after.Attribution.Actor != actor || after.ID != before.ID || after.Type != before.Type || !reflect.DeepEqual(after.Owned, before.Owned) || after.Properties.RowVersion == before.Properties.RowVersion {
		t.Fatal("reference transition did not preserve complete expected identity, values, version, attribution and owned state")
	}
	properties := *after.Properties
	properties.ExternalRef, properties.SpecID, properties.Title = before.Properties.ExternalRef, before.Properties.SpecID, before.Properties.Title
	properties.UpdatedAt, properties.RowVersion, properties.ContentHash = before.Properties.UpdatedAt, before.Properties.RowVersion, before.Properties.ContentHash
	if !reflect.DeepEqual(properties, *before.Properties) {
		t.Fatal("reference update changed unrelated properties, lease or lifecycle state")
	}
}

func TestIssueReferencesLifecycle(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s, original, target, dependency := reopenFixture(t, backend)
			memory, err := s.Create(ctx, CreateRequest{Path: "beads/context", Body: "Unchanged reference context"})
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
			if original.Properties.ExternalRef != nil || original.Properties.SpecID != "" {
				t.Fatal("fixture must start with absent references")
			}
			current, versions := original, 2
			change := func(request UpdateIssueRequest, external *string, spec, title string) {
				t.Helper()
				request.Path, request.Actor = "beads/work", "reference-editor"
				if !request.Unconditional {
					request.ExpectedRevision = current.Revision
				}
				got, err := s.UpdateIssue(ctx, request)
				if err != nil || !got.Changed {
					t.Fatalf("reference update: changed=%v error=%v", got.Changed, err)
				}
				assertIssueReferencesTransition(t, current, got.Issue, external, spec, title, request.Actor)
				assertIssueEditVersion(t, ctx, s, "beads/work", current)
				assertIssueEditVersion(t, ctx, s, "beads/work", got.Issue)
				current, versions = got.Issue, versions+1
				assertIssueEditCounts(t, ctx, s, original.Properties.ID, versions)
			}
			external, spec := "  JIRA-É/雪\r\n", " spec 😀\t\r\n"
			change(UpdateIssueRequest{ExternalRef: &external, SpecID: &spec}, &external, spec, current.Properties.Title)
			// A same-value edit by a different actor must not rewrite attribution,
			// coordinate a write or record another version.
			unchangedState := reopenState(t, ctx, s)
			s.afterWrite = func(stage string) error { return errors.New("reference no-op reached " + stage) }
			unchanged, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "other", ExpectedRevision: current.Revision, ExternalRef: &external, SpecID: &spec})
			s.afterWrite = nil
			if err != nil || unchanged.Changed || !reflect.DeepEqual(unchanged.Issue, current) || !reflect.DeepEqual(unchangedState, reopenState(t, ctx, s)) {
				t.Fatalf("same references no-op: changed=%v error=%v", unchanged.Changed, err)
			}
			// Literal values are not URL-parsed, normalized, case-folded or interpreted
			// as file/stdin input. Omitted fields preserve the actual predecessor.
			change(UpdateIssueRequest{ExternalRef: issueEditString("-")}, issueEditString("-"), spec, current.Properties.Title)
			change(UpdateIssueRequest{SpecID: issueEditString("-")}, current.Properties.ExternalRef, "-", current.Properties.Title)
			for _, character := range []string{"x", "😀"} {
				external, spec := strings.Repeat(character, 255), strings.Repeat(character, 1024)
				change(UpdateIssueRequest{ExternalRef: &external, SpecID: &spec}, &external, spec, current.Properties.Title)
			}
			external, spec = "copied external", "copied spec"
			s.afterWrite = func(stage string) error {
				if stage == "coordination" {
					external, spec = "mutated caller external", "mutated caller spec"
				}
				return nil
			}
			change(UpdateIssueRequest{ExternalRef: &external, SpecID: &spec, Title: issueEditString("Reference pair updated")}, issueEditString("copied external"), "copied spec", "Reference pair updated")
			s.afterWrite = nil
			change(UpdateIssueRequest{Title: issueEditString("References omitted")}, current.Properties.ExternalRef, current.Properties.SpecID, "References omitted")
			change(UpdateIssueRequest{Unconditional: true, ExternalRef: issueEditString(""), SpecID: issueEditString("")}, nil, "", current.Properties.Title)
			// External empty is SQL NULL, not an empty-but-present reference. Spec's
			// inherited clear is an empty string, even though both omit from JSON.
			var externalNull, specNull bool
			var storedSpec string
			if err := s.db.QueryRowContext(ctx, "SELECT external_ref IS NULL, spec_id IS NULL, spec_id FROM issues WHERE id=?", original.Properties.ID).Scan(&externalNull, &specNull, &storedSpec); err != nil || !externalNull || specNull || storedSpec != "" {
				t.Fatalf("clear representation: externalNull=%v specNull=%v spec=%q error=%v", externalNull, specNull, storedSpec, err)
			}
			raw, err := json.Marshal(current.Properties)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			if _, ok := fields["external_ref"]; ok {
				t.Fatal("cleared external_ref must be omitted from JSON")
			}
			if _, ok := fields["spec_id"]; ok {
				t.Fatal("cleared spec_id must be omitted from JSON")
			}
			state := reopenState(t, ctx, s)
			s.afterWrite = func(stage string) error { return errors.New("reference no-op reached " + stage) }
			noop, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "different-actor", ExpectedRevision: current.Revision, ExternalRef: issueEditString(""), SpecID: issueEditString("")})
			s.afterWrite = nil
			if err != nil || noop.Changed || !reflect.DeepEqual(noop.Issue, current) || !reflect.DeepEqual(state, reopenState(t, ctx, s)) {
				t.Fatalf("repeated clear no-op: changed=%v error=%v", noop.Changed, err)
			}
			stale, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "stale", ExpectedRevision: original.Revision, ExternalRef: issueEditString(""), SpecID: issueEditString("")})
			if !errors.Is(err, ErrConflict) || !reflect.DeepEqual(stale, IssueMutationResult{}) || !reflect.DeepEqual(state, reopenState(t, ctx, s)) {
				t.Fatalf("stale equal clear bypassed guard: %v", err)
			}
			if got, err := s.Read(ctx, "beads/work"); err != nil || !reflect.DeepEqual(got, current) {
				t.Fatalf("current complete references: %v", err)
			}
			if got, err := s.Read(ctx, "beads/context"); err != nil || !reflect.DeepEqual(got, memory) {
				t.Fatalf("Memory changed: %v", err)
			}
			if got, err := s.ShowIssue(ctx, "beads/prereq"); err != nil || !reflect.DeepEqual(got, target) {
				t.Fatalf("target changed: %v", err)
			}
			for path, want := range map[string]LinkRecord{"links/block": dependency, "links/context": info.Link} {
				if got, err := s.ShowLink(ctx, path); err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("Link %s changed: %v", path, err)
				}
			}
			if got, err := s.ReadyIssues(ctx); err != nil || !reflect.DeepEqual(got, readyBefore) {
				t.Fatalf("readiness changed: %v", err)
			}
		})
	}
}

func TestIssueReferencesClaimedAndClosed(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s, original, _, _ := reopenFixture(t, backend)
			claimed, err := s.ClaimIssue(ctx, "beads/work", "holder")
			if err != nil || !claimed.Changed {
				t.Fatalf("claim: %v", err)
			}
			got, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "holder", ExpectedRevision: claimed.Issue.Revision, ExternalRef: issueEditString("claimed-ref"), SpecID: issueEditString("claimed-spec")})
			if err != nil || !got.Changed {
				t.Fatalf("claimed reference update: %v", err)
			}
			assertIssueReferencesTransition(t, claimed.Issue, got.Issue, issueEditString("claimed-ref"), "claimed-spec", claimed.Issue.Properties.Title, "holder")
			assertClaimLease(t, ctx, s, original.Properties.ID, "holder")
			assertIssueEditVersion(t, ctx, s, "beads/work", claimed.Issue)
			assertIssueEditCounts(t, ctx, s, original.Properties.ID, 4)
			if _, err := s.CloseIssue(ctx, "beads/prereq", "done", "reviewer"); err != nil {
				t.Fatal(err)
			}
			closed, err := s.CloseIssue(ctx, "beads/work", "Delivered", "holder")
			if err != nil || !closed.Changed {
				t.Fatalf("close: %v", err)
			}
			got, err = s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "holder", ExpectedRevision: closed.Issue.Revision, ExternalRef: issueEditString(""), SpecID: issueEditString("closed-spec")})
			if err != nil || !got.Changed {
				t.Fatalf("closed reference update: %v", err)
			}
			assertIssueReferencesTransition(t, closed.Issue, got.Issue, nil, "closed-spec", closed.Issue.Properties.Title, "holder")
			assertIssueEditCounts(t, ctx, s, original.Properties.ID, 6)
			assertIssueEditVersion(t, ctx, s, "beads/work", closed.Issue)
			assertIssueEditVersion(t, ctx, s, "beads/work", got.Issue)
			assertAssigneeLeaseCount(t, ctx, s, original.Properties.ID, 0)
			assertReadyIDs(t, ctx, s)
		})
	}
}

func TestIssueReferencesRefusalAndRollback(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s, source, _, _ := reopenFixture(t, backend)
			state := reopenState(t, ctx, s)
			for _, tc := range []struct {
				name           string
				external, spec *string
			}{
				{"external-utf8", issueEditString(string([]byte{0xff})), nil},
				{"spec-utf8", nil, issueEditString(string([]byte{0xff}))},
				{"external-ascii-overflow", issueEditString(strings.Repeat("x", 256)), nil},
				{"external-multibyte-overflow", issueEditString(strings.Repeat("😀", 256)), nil},
				{"spec-ascii-overflow", nil, issueEditString(strings.Repeat("x", 1025))},
				{"spec-multibyte-overflow", nil, issueEditString(strings.Repeat("😀", 1025))},
			} {
				t.Run(tc.name, func(t *testing.T) {
					got, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "invalid", ExpectedRevision: source.Revision, ExternalRef: tc.external, SpecID: tc.spec, Title: issueEditString("Must not change")})
					if strings.HasPrefix(tc.name, "external-") && strings.HasSuffix(tc.name, "overflow") && !errors.Is(err, types.ErrFieldTooLong) {
						t.Fatalf("external reference lost the shared length error: %v", err)
					}
					if !errors.Is(err, storage.ErrValidation) || !reflect.DeepEqual(got, IssueMutationResult{}) || !reflect.DeepEqual(state, reopenState(t, ctx, s)) {
						t.Fatalf("invalid reference did not refuse atomically: %v", err)
					}
				})
			}
			for _, stage := range []string{"coordination", "issue-update", "issue-retained", "source-catalog", "source-retained"} {
				t.Run(stage, func(t *testing.T) {
					fault := errors.New("reference rollback")
					s.afterWrite = func(at string) error {
						if at == stage {
							return fault
						}
						return nil
					}
					got, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "fault", ExpectedRevision: source.Revision, ExternalRef: issueEditString("ref"), SpecID: issueEditString("spec"), Title: issueEditString("Must roll back")})
					s.afterWrite = nil
					if !errors.Is(err, fault) || !reflect.DeepEqual(got, IssueMutationResult{}) || !reflect.DeepEqual(state, reopenState(t, ctx, s)) {
						t.Fatalf("reference rollback: %v", err)
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
			got, err := s.UpdateIssue(canceled, UpdateIssueRequest{Path: "beads/work", Actor: "cancel", ExpectedRevision: source.Revision, ExternalRef: issueEditString("ref"), SpecID: issueEditString("spec")})
			cancel()
			s.afterWrite = nil
			if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, IssueMutationResult{}) || !reflect.DeepEqual(state, reopenState(t, ctx, s)) {
				t.Fatalf("reference cancellation: %v", err)
			}
			assertIssueEditVersion(t, ctx, s, "beads/work", source)
		})
	}
}

func TestIssueReferencesConcurrentTextWriter(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			base, options, first, source, target, dependency := reopenFixture(t, backend)
			ctx, cancel := context.WithTimeout(base, time.Minute)
			defer cancel()
			second := &Store{db: first.db, options: options}
			if backend == "server" {
				var err error
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
				// Only server forces independent transaction overlap. Embedded
				// retains its production single-session pool with concurrent callers.
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
				actor := []string{"reference-writer", "text-writer"}[i]
				go func() {
					defer writers.Done()
					request := UpdateIssueRequest{Path: "beads/work", Actor: actor, ExpectedRevision: source.Revision}
					if actor == "reference-writer" {
						request.ExternalRef = issueEditString("competing-ref")
						request.SpecID = issueEditString("competing-spec")
					} else {
						request.Title = issueEditString("Competing title")
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
			var winner outcome
			conflicts := 0
			for range 2 {
				select {
				case got := <-results:
					if got.err == nil {
						if winner.actor != "" || !got.value.Changed {
							t.Fatalf("not one changed winner: %+v", got)
						}
						winner = got
					} else if errors.Is(got.err, ErrConflict) && !errors.Is(got.err, ErrOutcomeUnknown) && reflect.DeepEqual(got.value, IssueMutationResult{}) {
						conflicts++
					} else {
						t.Fatalf("bad losing outcome: %+v", got)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			writers.Wait()
			first.afterWrite = nil
			second.afterWrite = nil
			if winner.actor == "" || conflicts != 1 {
				t.Fatalf("winner=%s conflicts=%d", winner.actor, conflicts)
			}
			current, err := first.Read(ctx, "beads/work")
			if err != nil || !reflect.DeepEqual(current, winner.value.Issue) {
				t.Fatalf("not complete winner: %+v %v", current, err)
			}
			external, spec, title := source.Properties.ExternalRef, source.Properties.SpecID, source.Properties.Title
			if winner.actor == "reference-writer" {
				external, spec = issueEditString("competing-ref"), "competing-spec"
			} else {
				title = "Competing title"
			}
			assertIssueReferencesTransition(t, source, winner.value.Issue, external, spec, title, winner.actor)
			for _, actor := range []string{"reference-writer", "text-writer"} {
				want := 0
				if actor == winner.actor {
					want = 1
				}
				var events int
				if err := first.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE issue_id=? AND actor=?", source.Properties.ID, actor).Scan(&events); err != nil || events != want {
					t.Fatalf("audit actor%s count%d want%d: %v", actor, events, want, err)
				}
			}
			var afterEvents int
			if err := first.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE issue_id=?", source.Properties.ID).Scan(&afterEvents); err != nil || afterEvents != beforeEvents+1 {
				t.Fatalf("audit count %d->%d: %v", beforeEvents, afterEvents, err)
			}
			assertIssueEditCounts(t, ctx, first, source.Properties.ID, 3)
			assertIssueEditVersion(t, ctx, first, "beads/work", source)
			assertIssueEditVersion(t, ctx, first, "beads/work", winner.value.Issue)
			if got, err := first.ShowIssue(ctx, "beads/prereq"); err != nil || !reflect.DeepEqual(got, target) {
				t.Fatalf("target changed: %+v %v", got, err)
			}
			if got, err := first.ShowLink(ctx, "links/block"); err != nil || !reflect.DeepEqual(got, dependency) {
				t.Fatalf("Dependency changed: %+v %v", got, err)
			}
		})
	}
}
