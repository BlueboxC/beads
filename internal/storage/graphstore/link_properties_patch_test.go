//go:build cgo

package graphstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/graphpatch"
	"github.com/steveyegge/beads/internal/storage"
)

func linkNotePatch(t *testing.T, value string) []byte {
	t.Helper()
	raw, err := json.Marshal([]map[string]any{{"op": "add", "path": "/note", "value": value}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func assertLinkPatchTransition(t *testing.T, before LinkRecord, source Record, got LinkMutationResult, properties map[string]any, actor string, disclose bool) Record {
	t.Helper()
	expected := before
	expected.Properties = properties
	expected.Version, expected.Revision, expected.Attribution = got.Link.Version, got.Link.Revision, got.Link.Attribution
	if !got.Changed || got.Link.Version == before.Version || got.Link.Version != got.Link.Revision || !reflect.DeepEqual(got.Link, expected) || got.Link.Attribution.Actor != actor {
		t.Fatalf("incomplete Link transition: %+v", got)
	}
	memory, ok := got.Source.(Record)
	if !ok {
		t.Fatalf("source is %T", got.Source)
	}
	expectedSource := source
	expectedSource.Version, expectedSource.Revision, expectedSource.Attribution = memory.Version, memory.Revision, memory.Attribution
	expectedSource.Owned = append([]json.RawMessage(nil), source.Owned...)
	found := false
	for i := range expectedSource.Owned {
		var old LinkRecord
		if err := json.Unmarshal(expectedSource.Owned[i], &old); err != nil {
			t.Fatal(err)
		}
		if old.ID == before.ID {
			raw, err := canonicalJSON(got.Link)
			if err != nil {
				t.Fatal(err)
			}
			expectedSource.Owned[i] = raw
			found = true
		}
	}
	if !found {
		t.Fatal("fixture is missing owned Link")
	}
	if memory.Version == source.Version || memory.Version != memory.Revision || memory.Attribution.Actor != actor || !reflect.DeepEqual(memory, expectedSource) {
		t.Fatalf("incomplete owned source: %+v", got)
	}
	if disclose {
		assertReplacedMemory(t, got.ReplacedSource, source)
	} else if got.ReplacedSource != nil {
		t.Fatal("guarded source disclosed replacement")
	}
	return memory
}

func assertLinkPatchSaved(t *testing.T, ctx context.Context, s *Store, path string, records ...LinkRecord) {
	t.Helper()
	for _, want := range records {
		got, err := s.ReadVersion(ctx, path, want.Version)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("saved Link %s: %+v %v", want.Version, got, err)
		}
	}
}

func TestLinkPropertiesPatchLifecycle(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s, original, target, start := disclosureFixture(t, backend)
			source, link := original, start.Link
			sources, links := []Record{source}, []LinkRecord{link}
			for i, tc := range []struct {
				patch                                  []byte
				want                                   map[string]any
				sourceUnconditional, linkUnconditional bool
			}{
				{linkNotePatch(t, "  雪\r\n"), map[string]any{"note": "  雪\r\n"}, false, true},
				{[]byte(`[{"op":"replace","path":"","value":{"note":"","scratch":[0]}},{"op":"add","path":"/scratch/-","value":1},{"op":"remove","path":"/scratch"}]`), map[string]any{"note": ""}, true, false},
				{[]byte(`[{"op":"remove","path":"/note"}]`), map[string]any{}, false, false},
			} {
				request := LinkPropertiesPatchRequest{Path: "links/out", Patch: tc.patch, Actor: "patch-author", Unconditional: tc.linkUnconditional, UnconditionalSource: tc.sourceUnconditional}
				if !tc.linkUnconditional {
					request.ExpectedRevision = link.Revision
				}
				if !tc.sourceUnconditional {
					request.ExpectedSourceRevision = source.Revision
				}
				got, err := s.PatchLinkProperties(ctx, request)
				if err != nil {
					t.Fatalf("step %d: %v", i, err)
				}
				source = assertLinkPatchTransition(t, link, source, got, tc.want, "patch-author", tc.sourceUnconditional)
				link = got.Link
				sources, links = append(sources, source), append(links, link)
			}
			before := workflowState(t, ctx, s)
			for _, unconditional := range []bool{false, true} {
				request := LinkPropertiesPatchRequest{Path: "links/out", Patch: []byte(`[{"op":"add","path":"/note","value":"temporary"},{"op":"remove","path":"/note"}]`), Actor: "different-noop", Unconditional: unconditional, UnconditionalSource: unconditional}
				if !unconditional {
					request.ExpectedRevision, request.ExpectedSourceRevision = link.Revision, source.Revision
				}
				got, err := s.PatchLinkProperties(ctx, request)
				if err != nil || got.Changed || got.ReplacedSource != nil || !reflect.DeepEqual(got.Link, link) || !reflect.DeepEqual(got.Source, source) {
					t.Fatalf("no-op: %+v %v", got, err)
				}
			}
			if !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
				t.Fatal("no-op changed retained/current state")
			}
			// The operation input is captured before coordination; later caller mutation
			// cannot substitute another operation into the accepted transaction.
			captured := linkNotePatch(t, "captured")
			s.afterWrite = func(stage string) error {
				if stage == "coordination" {
					for i := range captured {
						captured[i] = 'x'
					}
				}
				return nil
			}
			got, err := s.PatchLinkProperties(ctx, LinkPropertiesPatchRequest{Path: "links/out", Patch: captured, Actor: "capture", Unconditional: true, UnconditionalSource: true})
			s.afterWrite = nil
			if err != nil {
				t.Fatal(err)
			}
			source = assertLinkPatchTransition(t, link, source, got, map[string]any{"note": "captured"}, "capture", true)
			link = got.Link
			sources, links = append(sources, source), append(links, link)
			for _, want := range sources {
				assertRetainedMemory(t, ctx, s, "beads/plan", want)
				if saved, err := s.ReadVersion(ctx, "beads/plan", want.Version); err != nil || !reflect.DeepEqual(saved, want) {
					t.Fatalf("public exact source: %v", err)
				}
			}
			assertLinkPatchSaved(t, ctx, s, "links/out", links...)
			for path, want := range map[string]int{"beads/plan": len(sources) + 1, "links/out": len(links)} {
				var n int
				if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM graph_preview_versions WHERE path=?", path).Scan(&n); err != nil || n != want {
					t.Fatalf("retained %s=%d want%d: %v", path, n, want, err)
				}
			}
			if current, err := s.Show(ctx, "beads/plan"); err != nil || !reflect.DeepEqual(current, source) {
				t.Fatalf("current source: %v", err)
			}
			if current, err := s.ShowLink(ctx, "links/out"); err != nil || !reflect.DeepEqual(current, link) {
				t.Fatalf("current Link: %v", err)
			}
			if current, err := s.Show(ctx, "beads/target"); err != nil || !reflect.DeepEqual(current, target) {
				t.Fatalf("target changed: %v", err)
			}
			// Issue sources are unowned; even explicit source-unconditional must neither
			// mint a source version nor manufacture an R14 Memory disclosure.
			issue, err := s.CreateIssue(ctx, "beads/work", plainIssue("Work"))
			if err != nil {
				t.Fatal(err)
			}
			issueLink, err := s.AddInformationalLink(ctx, LinkCreateRequest{Path: "links/context", SourcePath: "beads/work", TargetPath: "beads/target"})
			if err != nil {
				t.Fatal(err)
			}
			for _, guard := range []string{"none", "expected", "unconditional"} {
				req := LinkPropertiesPatchRequest{Path: "links/context", Patch: linkNotePatch(t, guard), ExpectedRevision: issueLink.Link.Revision}
				if guard == "expected" {
					req.ExpectedSourceRevision = issue.Revision
				}
				if guard == "unconditional" {
					req.UnconditionalSource = true
				}
				updated, err := s.PatchLinkProperties(ctx, req)
				if err != nil || !updated.Changed || updated.ReplacedSource != nil || !reflect.DeepEqual(updated.Source, issue) {
					t.Fatalf("Issue source %s: %+v %v", guard, updated, err)
				}
				issueLink = updated
			}
			if current, err := s.ShowIssue(ctx, "beads/work"); err != nil || !reflect.DeepEqual(current, issue) {
				t.Fatalf("Issue source changed: %+v %v", current, err)
			}
			before = workflowState(t, ctx, s)
			bad, err := s.PatchLinkProperties(ctx, LinkPropertiesPatchRequest{Path: "links/context", Patch: linkNotePatch(t, "unconditional"), Unconditional: true, ExpectedSourceRevision: "stale"})
			if !errors.Is(err, ErrConflict) || !reflect.ValueOf(bad).IsZero() || !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
				t.Fatalf("optional Issue guard: %+v %v", bad, err)
			}
			// Equal endpoints remain distinct identities; a Memory self-Link
			// participates once in the owned set and mints one source revision.
			for _, tc := range []struct{ path, target string }{{"links/parallel", "beads/target"}, {"links/self", "beads/plan"}} {
				created, err := s.AddInformationalLink(ctx, LinkCreateRequest{Path: tc.path, SourcePath: "beads/plan", TargetPath: tc.target, ExpectedSourceRevision: source.Revision})
				if err != nil {
					t.Fatal(err)
				}
				predecessor := created.Source.(Record)
				updated, err := s.PatchLinkProperties(ctx, LinkPropertiesPatchRequest{Path: tc.path, Patch: linkNotePatch(t, "independent"), ExpectedRevision: created.Link.Revision, ExpectedSourceRevision: predecessor.Revision, Actor: "identity"})
				if err != nil {
					t.Fatal(err)
				}
				source = assertLinkPatchTransition(t, created.Link, predecessor, updated, map[string]any{"note": "independent"}, "identity", false)
				assertRetainedMemory(t, ctx, s, "beads/plan", predecessor)
				assertLinkPatchSaved(t, ctx, s, tc.path, created.Link, updated.Link)
				if unchanged, err := s.ShowLink(ctx, "links/out"); err != nil || !reflect.DeepEqual(unchanged, link) {
					t.Fatalf("other Link changed: %v", err)
				}
			}
		})
	}
}

func TestLinkPropertiesPatchRefusalAndRollback(t *testing.T) {
	t.Run("mixed-mode", func(t *testing.T) {
		patch, err := graphpatch.Parse([]byte(`[{"op":"add","path":"/note","value":"x"}]`))
		if err != nil {
			t.Fatal(err)
		}
		var s *Store
		got, err := s.writeLinkProperties(context.Background(), LinkUpdateRequest{}, []byte(`{}`), patch)
		if !errors.Is(err, storage.ErrValidation) || !reflect.ValueOf(got).IsZero() {
			t.Fatalf("mixed mode: %+v %v", got, err)
		}
	})
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s, original, _, link := disclosureFixture(t, backend)
			for _, path := range []string{"beads/work", "beads/prereq"} {
				if _, err := s.CreateIssue(ctx, path, plainIssue(path)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.AddDependency(ctx, DependencyRequest{Path: "links/block", SourcePath: "beads/work", TargetPath: "beads/prereq", Actor: "fixture"}); err != nil {
				t.Fatal(err)
			}
			before := workflowState(t, ctx, s)
			request := LinkPropertiesPatchRequest{Path: "links/out", Patch: linkNotePatch(t, "changed"), Actor: "writer", ExpectedRevision: link.Link.Revision, ExpectedSourceRevision: original.Revision}
			failure := func(t *testing.T, got LinkMutationResult, err, want error) {
				t.Helper()
				if !errors.Is(err, want) || !reflect.ValueOf(got).IsZero() {
					t.Fatalf("refusal: %+v %v want%v", got, err, want)
				}
				if !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
					t.Fatal("refusal changed complete state")
				}
			}
			for _, tc := range []struct{ name, patch string }{
				{"empty-operations", `[]`}, {"missing-note", `[{"op":"replace","path":"/note","value":"x"}]`}, {"null-note", `[{"op":"add","path":"/note","value":null}]`}, {"extra-member", `[{"op":"add","path":"/title","value":"x"}]`}, {"array-final", `[{"op":"replace","path":"","value":[]}]`}, {"late-error", `[{"op":"add","path":"/note","value":"early"},{"op":"remove","path":"/missing"}]`},
			} {
				t.Run(tc.name, func(t *testing.T) {
					r := request
					r.Patch = []byte(tc.patch)
					got, err := s.PatchLinkProperties(ctx, r)
					failure(t, got, err, storage.ErrValidation)
				})
			}
			for _, tc := range []struct {
				name   string
				change func(*LinkPropertiesPatchRequest)
				want   error
			}{
				{"actor", func(r *LinkPropertiesPatchRequest) { r.Actor = "\xff" }, storage.ErrValidation},
				{"missing", func(r *LinkPropertiesPatchRequest) { r.Path = "links/missing" }, ErrNotFound},
				{"blocking-Type", func(r *LinkPropertiesPatchRequest) { r.Path = "links/block" }, storage.ErrValidation},
				{"both-link-guards", func(r *LinkPropertiesPatchRequest) { r.Unconditional = true }, storage.ErrValidation},
				{"neither-link-guard", func(r *LinkPropertiesPatchRequest) { r.ExpectedRevision = "" }, storage.ErrValidation},
				{"both-source-guards", func(r *LinkPropertiesPatchRequest) { r.UnconditionalSource = true }, storage.ErrValidation},
				{"neither-source-guard", func(r *LinkPropertiesPatchRequest) { r.ExpectedSourceRevision = "" }, storage.ErrValidation},
				{"stale-link-before-apply", func(r *LinkPropertiesPatchRequest) {
					r.ExpectedRevision = "stale"
					r.Patch = []byte(`[{"op":"remove","path":"/missing"}]`)
				}, ErrConflict},
				{"stale-source-before-apply", func(r *LinkPropertiesPatchRequest) {
					r.ExpectedSourceRevision = "stale"
					r.Patch = []byte(`[{"op":"remove","path":"/missing"}]`)
				}, ErrConflict},
				{"stale-link-before-noop", func(r *LinkPropertiesPatchRequest) {
					r.ExpectedRevision = "stale"
					r.Patch = []byte(`[{"op":"replace","path":"","value":{}}]`)
				}, ErrConflict},
				{"stale-source-before-noop", func(r *LinkPropertiesPatchRequest) {
					r.ExpectedSourceRevision = "stale"
					r.Patch = []byte(`[{"op":"replace","path":"","value":{}}]`)
				}, ErrConflict},
				{"input-limit", func(r *LinkPropertiesPatchRequest) { r.Patch = []byte(strings.Repeat(" ", graphpatch.MaxInputBytes+1)) }, ErrLimitExceeded},
			} {
				t.Run(tc.name, func(t *testing.T) {
					r := request
					tc.change(&r)
					got, err := s.PatchLinkProperties(ctx, r)
					failure(t, got, err, tc.want)
				})
			}
			for _, stage := range []string{"coordination", "link-catalog", "link-payload", "link-retained", "source-catalog", "source-retained"} {
				t.Run("rollback-"+stage, func(t *testing.T) {
					sentinel := errors.New(stage)
					s.afterWrite = func(at string) error {
						if at == stage {
							return sentinel
						}
						return nil
					}
					got, err := s.PatchLinkProperties(ctx, request)
					s.afterWrite = nil
					failure(t, got, err, sentinel)
				})
			}
			t.Run("cancel", func(t *testing.T) {
				canceled, cancel := context.WithCancel(ctx)
				defer cancel()
				s.afterWrite = func(stage string) error {
					if stage == "source-retained" {
						cancel()
						return canceled.Err()
					}
					return nil
				}
				got, err := s.PatchLinkProperties(canceled, request)
				s.afterWrite = nil
				failure(t, got, err, context.Canceled)
			})
			t.Run("authority", func(t *testing.T) {
				old := s.options
				s.options.Binding.AuthorityID = "ffffffffffffffffffffffffffffffff"
				got, err := s.PatchLinkProperties(ctx, request)
				s.options = old
				failure(t, got, err, ErrInvalidStore)
			})
		})
	}
}

func TestLinkPropertiesPatchReadBudget(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s, original, target, link := disclosureFixture(t, backend)
			// The unchanged filler fits the established current-read budget. Multiple
			// copies of a changed Link (including its owner) would exceed that budget.
			if _, err := s.Create(ctx, CreateRequest{Path: "beads/filler", Body: strings.Repeat("f", PreviewCurrentReadByteLimit/2-(512<<10))}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Read(ctx, "links/out"); err != nil {
				t.Fatalf("fixture unreadable: %v", err)
			}
			before := workflowState(t, ctx, s)
			large := strings.Repeat("x", 768<<10)
			for _, unconditional := range []bool{false, true} {
				t.Run(fmt.Sprintf("unconditional-%t", unconditional), func(t *testing.T) {
					request := LinkPropertiesPatchRequest{Path: "links/out", Patch: linkNotePatch(t, large), Unconditional: unconditional, UnconditionalSource: unconditional}
					if !unconditional {
						request.ExpectedRevision, request.ExpectedSourceRevision = link.Link.Revision, original.Revision
					}
					got, err := s.PatchLinkProperties(ctx, request)
					if !errors.Is(err, ErrLimitExceeded) || !reflect.ValueOf(got).IsZero() {
						t.Fatalf("budget: changed=%t err=%v", got.Changed, err)
					}
					if !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
						t.Fatal("budget refusal leaked state")
					}
					if _, err := s.Read(ctx, "links/out"); err != nil {
						t.Fatalf("budget refusal broke current read: %v", err)
					}
				})
			}
			// Replacement obeys the same atomic budget as a patch.
			refused, err := s.UpdateLink(ctx, LinkUpdateRequest{Path: "links/out", Properties: map[string]any{"note": large}, ExpectedRevision: link.Link.Revision, ExpectedSourceRevision: original.Revision, Actor: "replacement"})
			if !errors.Is(err, ErrLimitExceeded) || !reflect.ValueOf(refused).IsZero() || !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
				t.Fatalf("replacement budget refusal: %+v %v", refused, err)
			}
			filler, err := s.UpdateMemory(ctx, MemoryUpdateRequest{Path: "beads/filler", Properties: Properties{Body: strings.Repeat("f", 6<<20)}, Unconditional: true})
			if err != nil {
				t.Fatal(err)
			}
			replacement, err := s.UpdateLink(ctx, LinkUpdateRequest{Path: "links/out", Properties: map[string]any{"note": large}, ExpectedRevision: link.Link.Revision, ExpectedSourceRevision: original.Revision, Actor: "replacement"})
			if err != nil || !replacement.Changed {
				t.Fatalf("bounded replacement: changed=%t err=%v", replacement.Changed, err)
			}
			replacementSource, ok := replacement.Source.(Record)
			if !ok {
				t.Fatalf("replacement source type %T", replacement.Source)
			}
			// Simulate canonical oversized state admitted by an older build.
			replaceLegacyFixtureMemory(t, ctx, s, "beads/filler", filler.Memory, strings.Repeat("f", PreviewCurrentReadByteLimit/2-(512<<10)))
			if _, err := s.Read(ctx, "links/out"); !errors.Is(err, ErrLimitExceeded) {
				t.Fatalf("legacy fixture must exceed current-read budget: %v", err)
			}
			oversizedState := workflowState(t, ctx, s)
			t.Run("oversized-noop", func(t *testing.T) {
				noop, err := s.PatchLinkProperties(ctx, LinkPropertiesPatchRequest{Path: "links/out", Patch: linkNotePatch(t, large), ExpectedRevision: replacement.Link.Revision, ExpectedSourceRevision: replacementSource.Revision, Actor: "different-noop"})
				if err != nil || noop.Changed || noop.ReplacedSource != nil || !reflect.DeepEqual(noop.Link, replacement.Link) || !reflect.DeepEqual(noop.Source, replacementSource) {
					t.Fatalf("oversized no-op: changed=%t err=%v", noop.Changed, err)
				}
				if !reflect.DeepEqual(oversizedState, workflowState(t, ctx, s)) {
					t.Fatal("oversized no-op changed complete state")
				}
				if _, err := s.Read(ctx, "links/out"); !errors.Is(err, ErrLimitExceeded) {
					t.Fatalf("no-op unexpectedly changed current-read budget: %v", err)
				}
			})
			t.Run("shrinking-recovery", func(t *testing.T) {
				recovered, err := s.PatchLinkProperties(ctx, LinkPropertiesPatchRequest{Path: "links/out", Patch: []byte(`[{"op":"remove","path":"/note"}]`), ExpectedRevision: replacement.Link.Revision, ExpectedSourceRevision: replacementSource.Revision, Actor: "recovery"})
				if err != nil {
					t.Fatal(err)
				}
				recoveredSource := assertLinkPatchTransition(t, replacement.Link, replacementSource, recovered, map[string]any{}, "recovery", false)
				for path, want := range map[string]any{"links/out": recovered.Link, "beads/plan": recoveredSource, "beads/target": target} {
					current, err := s.Read(ctx, path)
					if err != nil || !reflect.DeepEqual(current, want) {
						t.Fatalf("shrink did not restore complete current %s: %v", path, err)
					}
				}
				for _, want := range []Record{original, replacementSource, recoveredSource} {
					saved, err := s.ReadVersion(ctx, "beads/plan", want.Version)
					if err != nil || !reflect.DeepEqual(saved, want) {
						t.Fatalf("exact source version %s mismatch: %v", want.Version, err)
					}
				}
				for _, want := range []LinkRecord{link.Link, replacement.Link, recovered.Link} {
					saved, err := s.ReadVersion(ctx, "links/out", want.Version)
					if err != nil || !reflect.DeepEqual(saved, want) {
						t.Fatalf("exact Link version %s mismatch: %v", want.Version, err)
					}
				}
				if saved, err := s.ReadVersion(ctx, "beads/target", target.Version); err != nil || !reflect.DeepEqual(saved, target) {
					t.Fatalf("exact target changed: %v", err)
				}
				for path, want := range map[string]int{"links/out": 3, "beads/plan": 4, "beads/target": 1} {
					var count int
					if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM graph_preview_versions WHERE path=?", path).Scan(&count); err != nil || count != want {
						t.Fatalf("retained %s count=%d want%d: %v", path, count, want, err)
					}
				}
			})
		})
	}
}

func TestLinkPropertiesPatchConcurrentOwnedWriter(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		for _, unconditional := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/unconditional-%t", backend, unconditional), func(t *testing.T) {
				base, o, first, original, target, link := disclosureFixture(t, backend)
				ctx, cancel := context.WithCancel(base)
				defer cancel()
				second := &Store{db: first.db, options: o}
				if backend == "server" {
					var err error
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
				type outcome struct {
					kind          string
					memory        Record
					link          LinkRecord
					replaced      *ReplacedMemory
					changed, zero bool
					err           error
				}
				results := make(chan outcome, 2)
				reached, release := make(chan struct{}, 2), make(chan struct{})
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
				defer func() { cancel(); writers.Wait(); first.afterWrite = nil; second.afterWrite = nil }()
				patch := linkNotePatch(t, "patched")
				go func() {
					defer writers.Done()
					r := LinkPropertiesPatchRequest{Path: "links/out", Patch: patch, Actor: "patch", Unconditional: unconditional, UnconditionalSource: unconditional}
					if !unconditional {
						r.ExpectedRevision, r.ExpectedSourceRevision = link.Link.Revision, original.Revision
					}
					got, err := first.PatchLinkProperties(ctx, r)
					memory, _ := got.Source.(Record)
					results <- outcome{"patch", memory, got.Link, got.ReplacedSource, got.Changed, reflect.ValueOf(got).IsZero(), err}
				}()
				go func() {
					defer writers.Done()
					r := MemoryUpdateRequest{Path: "beads/plan", Properties: Properties{Title: original.Properties.Title, Body: "competing body"}, Actor: "memory", Unconditional: unconditional}
					if !unconditional {
						r.ExpectedRevision = original.Revision
					}
					got, err := second.UpdateMemory(ctx, r)
					results <- outcome{kind: "memory", memory: got.Memory, replaced: got.Replaced, changed: got.Changed, zero: reflect.ValueOf(got).IsZero(), err: err}
				}()
				// Production embedded pool1 serializes writers; only ordinary server forces
				// overlap. No extra embedded sessions or application locking are introduced.
				if backend == "server" {
					for range 2 {
						select {
						case <-reached:
						case got := <-results:
							t.Fatalf("writer before overlap: %+v", got)
						case <-ctx.Done():
							t.Fatal(ctx.Err())
						}
					}
					close(release)
				}
				successes := []outcome{}
				conflicts := 0
				for range 2 {
					select {
					case got := <-results:
						if got.err == nil {
							if !got.changed {
								t.Fatal("accepted writer unchanged")
							}
							successes = append(successes, got)
						} else if errors.Is(got.err, ErrConflict) && !errors.Is(got.err, ErrOutcomeUnknown) && got.zero {
							conflicts++
						} else {
							t.Fatalf("writer: %+v", got)
						}
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				}
				writers.Wait()
				first.afterWrite = nil
				second.afterWrite = nil
				wantSuccess := 1
				if backend == "embedded" && unconditional {
					wantSuccess = 2
				}
				if len(successes) != wantSuccess || conflicts != 2-wantSuccess {
					t.Fatalf("successes%d conflicts%d", len(successes), conflicts)
				}
				previous, currentLink := original, link.Link
				used := map[string]bool{}
				linkWins := 0
				for range len(successes) {
					found := false
					for _, got := range successes {
						if used[got.kind] || unconditional && (got.replaced == nil || got.replaced.Version != previous.Version) {
							continue
						}
						if got.kind == "patch" {
							result := LinkMutationResult{Link: got.link, Source: got.memory, Changed: got.changed, ReplacedSource: got.replaced}
							assertLinkPatchTransition(t, currentLink, previous, result, map[string]any{"note": "patched"}, "patch", unconditional)
							currentLink = got.link
							linkWins++
						} else {
							want := previous.Properties
							want.Body = "competing body"
							assertMemoryPropertiesTransition(t, previous, MemoryMutationResult{Memory: got.memory, Changed: got.changed, Replaced: got.replaced}, want, "memory", unconditional)
						}
						assertRetainedMemory(t, ctx, first, "beads/plan", got.memory)
						previous = got.memory
						used[got.kind] = true
						found = true
						break
					}
					if !found {
						t.Fatal("missing committed predecessor chain")
					}
				}
				if got, err := first.Show(ctx, "beads/plan"); err != nil || !reflect.DeepEqual(got, previous) {
					t.Fatalf("current source: %+v %v", got, err)
				}
				if got, err := first.ShowLink(ctx, "links/out"); err != nil || !reflect.DeepEqual(got, currentLink) {
					t.Fatalf("current Link: %+v %v", got, err)
				}
				if got, err := first.Show(ctx, "beads/target"); err != nil || !reflect.DeepEqual(got, target) {
					t.Fatalf("target: %+v %v", got, err)
				}
				assertRetainedMemory(t, ctx, first, "beads/plan", original)
				assertLinkPatchSaved(t, ctx, first, "links/out", link.Link, currentLink)
				for path, want := range map[string]int{"beads/plan": 2 + len(successes), "links/out": 1 + linkWins} {
					var count int
					if err := first.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM graph_preview_versions WHERE path=?", path).Scan(&count); err != nil || count != want {
						t.Fatalf("retained %s=%d want%d: %v", path, count, want, err)
					}
				}
				later, err := first.PatchLinkProperties(ctx, LinkPropertiesPatchRequest{Path: "links/out", Patch: linkNotePatch(t, "later"), Actor: "later", Unconditional: true, UnconditionalSource: true})
				if err != nil {
					t.Fatal(err)
				}
				assertLinkPatchTransition(t, currentLink, previous, later, map[string]any{"note": "later"}, "later", true)
			})
		}
	}
}

func TestLinkPropertiesPatchLostCommitResponse(t *testing.T) {
	ctx, o, direct, original, target, link := disclosureFixture(t, "server")
	port, observed := startCommitLossProxy(t, net.JoinHostPort(o.ServerHost, strconv.Itoa(o.ServerPort)))
	proxy := o
	proxy.ServerPort = port
	s, err := OpenExisting(ctx, proxy)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	got, err := s.PatchLinkProperties(ctx, LinkPropertiesPatchRequest{Path: "links/out", Patch: linkNotePatch(t, "uncertain"), Actor: "uncertain", Unconditional: true, UnconditionalSource: true})
	if !errors.Is(err, ErrOutcomeUnknown) || errors.Is(err, ErrConflict) || !reflect.ValueOf(got).IsZero() {
		t.Fatalf("lost COMMIT published receipt: %+v %v", got, err)
	}
	select {
	case packet := <-observed:
		if len(packet) == 0 || packet[0] != 0 {
			t.Fatalf("not a committed response: %x", packet)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	current, err := direct.Show(ctx, "beads/plan")
	if err != nil {
		t.Fatal(err)
	}
	currentLink, err := direct.ShowLink(ctx, "links/out")
	if err != nil {
		t.Fatal(err)
	}
	// The fault proxy alone witnessed the committed result; caller got no receipt
	// and no replay is attempted. Reconstruct a test-only expected transition.
	assertLinkPatchTransition(t, link.Link, original, LinkMutationResult{Link: currentLink, Source: current, Changed: true}, map[string]any{"note": "uncertain"}, "uncertain", false)
	assertRetainedMemory(t, ctx, direct, "beads/plan", original)
	assertRetainedMemory(t, ctx, direct, "beads/plan", current)
	assertLinkPatchSaved(t, ctx, direct, "links/out", link.Link, currentLink)
	if got, err := direct.Show(ctx, "beads/target"); err != nil || !reflect.DeepEqual(got, target) {
		t.Fatalf("target: %+v %v", got, err)
	}
	for path, want := range map[string]int{"beads/plan": 3, "links/out": 2} {
		var count int
		if err := direct.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM graph_preview_versions WHERE path=?", path).Scan(&count); err != nil || count != want {
			t.Fatalf("single commit/no replay %s=%d want%d: %v", path, count, want, err)
		}
	}
}

func TestLinkPropertiesPatchDeletedBudget(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s, original, target, link := disclosureFixture(t, backend)
			deleted := propertiesPatchDeletedFiller(t, ctx, s)
			if _, err := s.Read(ctx, "links/out"); err != nil {
				t.Fatalf("deleted filler fixture unreadable: %v", err)
			}
			before := workflowState(t, ctx, s)
			for _, unconditional := range []bool{false, true} {
				r := LinkPropertiesPatchRequest{Path: "links/out", Patch: linkNotePatch(t, strings.Repeat("x", 768<<10)), Unconditional: unconditional, UnconditionalSource: unconditional}
				if !unconditional {
					r.ExpectedRevision, r.ExpectedSourceRevision = link.Link.Revision, original.Revision
				}
				got, err := s.PatchLinkProperties(ctx, r)
				if !errors.Is(err, ErrLimitExceeded) || !reflect.ValueOf(got).IsZero() || !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
					t.Fatalf("deleted-head Link budget rollback: changed=%t err=%v", got.Changed, err)
				}
			}
			for path, want := range map[string]any{"links/out": link.Link, "beads/plan": original, "beads/target": target} {
				got, err := s.Read(ctx, path)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("budget refusal changed current %s: %v", path, err)
				}
			}
			for i, path := range []string{"beads/deleted-filler-a", "beads/deleted-filler-b"} {
				got, err := s.ReadVersion(ctx, path, deleted[i].Version)
				if err != nil || !reflect.DeepEqual(got, deleted[i]) {
					t.Fatalf("deleted retained head changed: %v", err)
				}
			}
		})
	}
}
