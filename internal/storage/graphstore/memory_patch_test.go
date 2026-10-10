//go:build cgo

package graphstore

import (
	"context"
	"errors"
	"net"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

func patchText(value string) *string { return &value }

func TestMemoryPatchValidation(t *testing.T) {
	// Invalid requests must be rejected before opening any transaction.
	var s *Store
	for _, request := range []MemoryPatchRequest{
		{Path: "beads/plan"},
		{Path: "links/no", Body: patchText("body")},
		{Path: "beads/plan", Title: patchText(string([]byte{0xff}))},
		{Path: "beads/plan", Body: patchText(string([]byte{0xff}))},
		{Path: "beads/plan", Body: patchText("body"), Actor: string([]byte{0xff})},
	} {
		got, err := s.PatchMemory(context.Background(), request)
		if !errors.Is(err, storage.ErrValidation) || !reflect.ValueOf(got).IsZero() {
			t.Fatalf("validation: %+v %v", got, err)
		}
	}
}

func TestMemoryPatchLifecycle(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s, original, target, link := disclosureFixture(t, backend)
			retained := []Record{original}
			title := "  記憶 title\r\n"
			titleEdit, err := s.PatchMemory(ctx, MemoryPatchRequest{Path: "beads/plan", Title: &title, Actor: "title-editor", ExpectedRevision: original.Revision})
			if err != nil || !titleEdit.Changed || titleEdit.Replaced != nil || titleEdit.Memory.Properties != (Properties{Title: title, Body: original.Properties.Body}) || !reflect.DeepEqual(titleEdit.Memory.Owned, original.Owned) {
				t.Fatalf("title-only: %+v %v", titleEdit, err)
			}
			retained = append(retained, titleEdit.Memory)
			// A partial unconditional write preserves the accepted title, not a value
			// from a caller's stale selection or an extra preliminary read.
			body := "---\r\nkind: memory\r\n---\r\n  雪 body\n\n"
			bodyEdit, err := s.PatchMemory(ctx, MemoryPatchRequest{Path: "beads/plan", Body: &body, Actor: "body-editor", Unconditional: true})
			if err != nil || !bodyEdit.Changed || bodyEdit.Memory.Properties != (Properties{Title: title, Body: body}) {
				t.Fatalf("body-only: %+v %v", bodyEdit, err)
			}
			assertReplacedMemory(t, bodyEdit.Replaced, titleEdit.Memory)
			retained = append(retained, bodyEdit.Memory)
			before := workflowState(t, ctx, s)
			for _, unconditional := range []bool{false, true} {
				request := MemoryPatchRequest{Path: "beads/plan", Body: &body, Actor: "different-actor", Unconditional: unconditional}
				if !unconditional {
					request.ExpectedRevision = bodyEdit.Memory.Revision
				}
				got, err := s.PatchMemory(ctx, request)
				if err != nil || got.Changed || got.Replaced != nil || !reflect.DeepEqual(got.Memory, bodyEdit.Memory) {
					t.Fatalf("no-op: %+v %v", got, err)
				}
			}
			if got, err := s.PatchMemory(ctx, MemoryPatchRequest{Path: "beads/plan", Body: &body, ExpectedRevision: titleEdit.Memory.Revision}); !errors.Is(err, ErrConflict) || !reflect.ValueOf(got).IsZero() {
				t.Fatalf("stale no-op: %+v %v", got, err)
			}
			if !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
				t.Fatal("no-op or stale request changed state")
			}
			changedLink, err := s.UpdateLink(ctx, LinkUpdateRequest{Path: "links/out", Properties: map[string]any{"note": "accepted"}, ExpectedRevision: link.Link.Revision, ExpectedSourceRevision: bodyEdit.Memory.Revision, Actor: "link-editor"})
			if err != nil {
				t.Fatal(err)
			}
			predecessor := changedLink.Source.(Record)
			retained = append(retained, predecessor)
			before = workflowState(t, ctx, s)
			if got, err := s.PatchMemory(ctx, MemoryPatchRequest{Path: "beads/plan", Body: &body, ExpectedRevision: bodyEdit.Memory.Revision}); !errors.Is(err, ErrConflict) || !reflect.ValueOf(got).IsZero() {
				t.Fatalf("owned-Link stale no-op: %+v %v", got, err)
			}
			if !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
				t.Fatal("owned-Link stale guard changed state")
			}
			clearedTitle, err := s.PatchMemory(ctx, MemoryPatchRequest{Path: "beads/plan", Title: patchText(""), Unconditional: true})
			if err != nil || !clearedTitle.Changed || clearedTitle.Memory.Properties != (Properties{Body: body}) {
				t.Fatalf("clear title: %+v %v", clearedTitle, err)
			}
			assertReplacedMemory(t, clearedTitle.Replaced, predecessor)
			assertOwnedLink(t, clearedTitle.Memory, changedLink.Link)
			retained = append(retained, clearedTitle.Memory)
			clearedBody, err := s.PatchMemory(ctx, MemoryPatchRequest{Path: "beads/plan", Body: patchText(""), ExpectedRevision: clearedTitle.Memory.Revision})
			if err != nil || !clearedBody.Changed || clearedBody.Replaced != nil || clearedBody.Memory.Properties != (Properties{}) {
				t.Fatalf("clear body: %+v %v", clearedBody, err)
			}
			retained = append(retained, clearedBody.Memory)
			// Mutate caller-owned pointer variables after the transaction starts. The
			// request must already have captured both values; no concurrent Go access.
			nextTitle, nextBody := "copied title", "copied body"
			s.afterWrite = func(stage string) error {
				if stage == "coordination" {
					nextTitle, nextBody = "too late", "too late"
				}
				return nil
			}
			both, err := s.PatchMemory(ctx, MemoryPatchRequest{Path: "beads/plan", Title: &nextTitle, Body: &nextBody, ExpectedRevision: clearedBody.Memory.Revision})
			s.afterWrite = nil
			if err != nil || !both.Changed || both.Memory.Properties != (Properties{Title: "copied title", Body: "copied body"}) {
				t.Fatalf("captured fields: %+v %v", both, err)
			}
			retained = append(retained, both.Memory)
			if got, err := s.Show(ctx, "beads/plan"); err != nil || !reflect.DeepEqual(got, both.Memory) {
				t.Fatalf("current: %+v %v", got, err)
			}
			if got, err := s.Show(ctx, "beads/target"); err != nil || !reflect.DeepEqual(got, target) {
				t.Fatalf("target: %+v %v", got, err)
			}
			if got, err := s.ShowLink(ctx, "links/out"); err != nil || !reflect.DeepEqual(got, changedLink.Link) {
				t.Fatalf("Link: %+v %v", got, err)
			}
			for _, want := range retained {
				if got, err := s.ReadVersion(ctx, "beads/plan", want.Version); err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("retained %s: %+v %v", want.Version, got, err)
				}
			}
			// The fixture also retains the initial pre-Link Memory.
			var count int
			if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM graph_preview_versions WHERE path='beads/plan'").Scan(&count); err != nil || count != len(retained)+1 {
				t.Fatalf("versions=%d want=%d: %v", count, len(retained)+1, err)
			}
		})
	}
}

func TestMemoryPatchFailureAtomicity(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s, original, _, _ := disclosureFixture(t, backend)
			if _, err := s.CreateIssue(ctx, "beads/work", plainIssue("Work")); err != nil {
				t.Fatal(err)
			}
			request := MemoryPatchRequest{Path: "beads/plan", Title: patchText("changed"), Unconditional: true}
			before := workflowState(t, ctx, s)
			assertFailure := func(t *testing.T, got MemoryMutationResult, err, want error) {
				t.Helper()
				if !errors.Is(err, want) || !reflect.ValueOf(got).IsZero() {
					t.Fatalf("failure: %+v %v want %v", got, err, want)
				}
				if !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
					t.Fatal("failure changed state")
				}
			}
			for _, bad := range []struct {
				path string
				want error
			}{{"beads/work", ErrCapabilityUnavailable}, {"beads/missing", ErrNotFound}} {
				attempt := request
				attempt.Path = bad.path
				got, err := s.PatchMemory(ctx, attempt)
				assertFailure(t, got, err, bad.want)
			}
			for _, guards := range []struct {
				name             string
				expectedRevision string
				unconditional    bool
			}{
				{"both-guards", original.Revision, true},
				{"neither-guard", "", false},
			} {
				t.Run(guards.name, func(t *testing.T) {
					attempt := request
					attempt.ExpectedRevision = guards.expectedRevision
					attempt.Unconditional = guards.unconditional
					got, err := s.PatchMemory(ctx, attempt)
					assertFailure(t, got, err, storage.ErrValidation)
				})
			}
			for _, stage := range []string{"coordination", "memory-payload", "source-catalog", "source-retained"} {
				injected := errors.New("patch rollback " + stage)
				s.afterWrite = func(actual string) error {
					if actual == stage {
						return injected
					}
					return nil
				}
				got, err := s.PatchMemory(ctx, request)
				s.afterWrite = nil
				assertFailure(t, got, err, injected)
			}
			canceled, cancel := context.WithCancel(ctx)
			s.afterWrite = func(stage string) error {
				if stage == "memory-payload" {
					cancel()
					return canceled.Err()
				}
				return nil
			}
			got, err := s.PatchMemory(canceled, request)
			cancel()
			s.afterWrite = nil
			assertFailure(t, got, err, context.Canceled)
			options := s.options
			s.options.Binding.AuthorityID = "ffffffffffffffffffffffffffffffff"
			got, err = s.PatchMemory(ctx, request)
			s.options = options
			assertFailure(t, got, err, ErrInvalidStore)
			if _, err := s.db.ExecContext(ctx, "UPDATE graph_preview_versions SET snapshot='{}' WHERE path='beads/plan'"); err != nil {
				t.Fatal(err)
			}
			before = workflowState(t, ctx, s)
			got, err = s.PatchMemory(ctx, request)
			assertFailure(t, got, err, ErrInvalidStore)
		})
	}
}

func TestMemoryPatchIndependentOfCurrentReadBudget(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s, original, _, _ := disclosureFixture(t, backend)
			// Its current payload and complete retained snapshot exceed the global
			// current-read acquisition budget, while this write's subject stays small.
			if _, err := s.Create(ctx, CreateRequest{Path: "beads/large", Body: strings.Repeat("x", PreviewCurrentReadByteLimit/2)}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Read(ctx, "beads/plan"); !errors.Is(err, ErrLimitExceeded) {
				t.Fatalf("global Read: %v", err)
			}
			got, err := s.PatchMemory(ctx, MemoryPatchRequest{Path: "beads/plan", Title: patchText("small target"), ExpectedRevision: original.Revision})
			if err != nil || !got.Changed || got.Memory.Properties != (Properties{Title: "small target", Body: original.Properties.Body}) || !reflect.DeepEqual(got.Memory.Owned, original.Owned) {
				t.Fatalf("subject-local patch: %+v %v", got, err)
			}
			if old, err := s.ReadVersion(ctx, "beads/plan", original.Version); err != nil || !reflect.DeepEqual(old, original) {
				t.Fatalf("retained: %+v %v", old, err)
			}
		})
	}
}

func TestMemoryPatchLostCommitResponse(t *testing.T) {
	ctx, o, direct, original, target, _ := disclosureFixture(t, "server")
	proxyPort, observed := startCommitLossProxy(t, net.JoinHostPort(o.ServerHost, strconv.Itoa(o.ServerPort)))
	proxied := o
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
	got, err := s.PatchMemory(ctx, MemoryPatchRequest{Path: "beads/plan", Title: patchText("uncertain"), Unconditional: true})
	if !errors.Is(err, ErrOutcomeUnknown) || errors.Is(err, ErrConflict) || !reflect.ValueOf(got).IsZero() {
		t.Fatalf("unknown receipt: %+v %v", got, err)
	}
	select {
	case packet := <-observed:
		if len(packet) == 0 || packet[0] != 0 {
			t.Fatalf("missing server commit witness: %x", packet)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// Only this fault harness knows the server committed; the writer returned no
	// result or predecessor disclosure and made no automatic retry.
	current, err := direct.Show(ctx, "beads/plan")
	if err != nil || current.Version == original.Version || current.Properties != (Properties{Title: "uncertain", Body: original.Properties.Body}) || !reflect.DeepEqual(current.Owned, original.Owned) {
		t.Fatalf("witnessed write: %+v %v", current, err)
	}
	if old, err := direct.ReadVersion(ctx, "beads/plan", original.Version); err != nil || !reflect.DeepEqual(old, original) {
		t.Fatalf("retained: %+v %v", old, err)
	}
	if got, err := direct.Show(ctx, "beads/target"); err != nil || !reflect.DeepEqual(got, target) {
		t.Fatalf("target: %+v %v", got, err)
	}
}

func TestMemoryPatchConcurrentWriters(t *testing.T) {
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
				memory, err := first.Create(ctx, CreateRequest{Path: "beads/plan", Title: "original title", Body: "original"})
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
					_, err := first.PatchMemory(ctx, MemoryPatchRequest{Path: "beads/plan", Body: patchText("writer-one"), ExpectedRevision: source.Revision})
					results <- err
				}()
				go func() {
					defer writers.Done()
					var err error
					if other == "memory" {
						_, err = second.UpdateMemory(ctx, MemoryUpdateRequest{Path: "beads/plan", Properties: Properties{Title: "replacement title", Body: "writer-two"}, ExpectedRevision: source.Revision})
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
				wantTitle := "original title"
				if current.Properties.Body == "writer-two" {
					wantTitle = "replacement title"
				}
				if current.Properties.Title != wantTitle {
					t.Fatalf("partial write lost omitted title: %+v", current.Properties)
				}
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

	for _, backend := range []string{"embedded", "server"} {
		for _, other := range []string{"memory", "owned-link"} {
			t.Run(backend+"/"+other+"/unconditional", func(t *testing.T) {
				baseCtx, o, first, original, target, originalLink := disclosureFixture(t, backend)
				// Give omission preservation an observable value before either writer starts.
				seeded, err := first.UpdateMemory(baseCtx, MemoryUpdateRequest{Path: "beads/plan", Properties: Properties{Title: "original title", Body: original.Properties.Body}, ExpectedRevision: original.Revision, Actor: "seed"})
				if err != nil {
					t.Fatal(err)
				}
				original = seeded.Memory
				ctx, cancel := context.WithCancel(baseCtx)
				defer cancel()
				// Embedded uses the production one-session pool: both unconditional calls
				// serialize and commit. Only ordinary server forces overlapping writes.
				second := &Store{db: first.db, options: o}
				if backend == "server" {
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
					operation string
					memory    Record
					replaced  *ReplacedMemory
					link      LinkRecord
					err       error
					zero      bool
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
				go func() {
					defer writers.Done()
					got, err := first.PatchMemory(ctx, MemoryPatchRequest{Path: "beads/plan", Body: patchText("patched body"), Actor: "patch-writer", Unconditional: true})
					results <- outcome{operation: "patch", memory: got.Memory, replaced: got.Replaced, err: err, zero: reflect.ValueOf(got).IsZero()}
				}()
				go func() {
					defer writers.Done()
					if other == "memory" {
						got, err := second.UpdateMemory(ctx, MemoryUpdateRequest{Path: "beads/plan", Properties: Properties{Title: "competing title", Body: "competing body"}, Actor: "other-writer", Unconditional: true})
						results <- outcome{operation: "memory", memory: got.Memory, replaced: got.Replaced, err: err, zero: reflect.ValueOf(got).IsZero()}
					} else {
						got, err := second.UpdateLink(ctx, LinkUpdateRequest{Path: "links/out", Properties: map[string]any{"note": "competing link"}, Actor: "other-writer", Unconditional: true, UnconditionalSource: true})
						memory, _ := got.Source.(Record)
						results <- outcome{operation: "owned-link", memory: memory, replaced: got.ReplacedSource, link: got.Link, err: err, zero: reflect.ValueOf(got).IsZero()}
					}
				}()
				if backend == "server" {
					for range 2 {
						select {
						case <-reached:
						case got := <-results:
							t.Fatalf("writer before forced overlap: %+v", got)
						case <-ctx.Done():
							t.Fatal(ctx.Err())
						}
					}
					close(release)
				}
				var successes []outcome
				conflicts := 0
				for range 2 {
					select {
					case got := <-results:
						if got.err == nil {
							successes = append(successes, got)
						} else if errors.Is(got.err, ErrConflict) && !errors.Is(got.err, ErrOutcomeUnknown) && got.zero {
							conflicts++
						} else {
							t.Fatalf("writer result/disclosure: %+v", got)
						}
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				}
				writers.Wait()
				first.afterWrite, second.afterWrite = nil, nil
				if backend == "server" && (len(successes) != 1 || conflicts != 1) {
					t.Fatalf("overlap successes=%d conflicts=%d", len(successes), conflicts)
				}
				if backend == "embedded" && (len(successes) != 2 || conflicts != 0) {
					t.Fatalf("serialized successes=%d conflicts=%d", len(successes), conflicts)
				}
				// Completion order is not commit order. Traverse the disclosed predecessor
				// identities, checking complete retained values and omitted fields at every
				// accepted transition instead of assuming which goroutine won.
				previous, currentLink := original, originalLink.Link
				for range len(successes) {
					found := false
					for _, got := range successes {
						if got.replaced == nil || got.replaced.Version != previous.Version {
							continue
						}
						assertReplacedMemory(t, got.replaced, previous)
						if saved, err := first.ReadVersion(ctx, "beads/plan", previous.Version); err != nil || !reflect.DeepEqual(saved, previous) {
							t.Fatalf("retained predecessor: %+v %v", saved, err)
						}
						wantProperties, wantOwned := previous.Properties, previous.Owned
						switch got.operation {
						case "patch":
							wantProperties.Body = "patched body"
						case "memory":
							wantProperties = Properties{Title: "competing title", Body: "competing body"}
						case "owned-link":
							currentLink = got.link
							// The fixture owns exactly this Link. Preserve complete Link identity,
							// properties, attribution and version through the source-owned snapshot.
							assertOwnedLink(t, got.memory, currentLink)
							wantOwned = got.memory.Owned
							if len(wantOwned) != 1 || currentLink.Properties["note"] != "competing link" || currentLink.Version == originalLink.Link.Version {
								t.Fatalf("incomplete Link transition: %+v", got)
							}
						}
						if got.memory.Properties != wantProperties || !reflect.DeepEqual(got.memory.Owned, wantOwned) || got.memory.ID != previous.ID || got.memory.Type != previous.Type || got.memory.Version == previous.Version {
							t.Fatalf("wrong accepted transition: before=%+v after=%+v", previous, got)
						}
						wantActor := "other-writer"
						if got.operation == "patch" {
							wantActor = "patch-writer"
						}
						if got.memory.Attribution.Actor != wantActor || got.memory.Attribution.Status != "claimed" {
							t.Fatalf("wrong accepted attribution: %+v", got)
						}
						if saved, err := first.ReadVersion(ctx, "beads/plan", got.memory.Version); err != nil || !reflect.DeepEqual(saved, got.memory) {
							t.Fatalf("retained accepted state: %+v %v", saved, err)
						}
						previous, found = got.memory, true
						break
					}
					if !found {
						t.Fatalf("no receipt successor for %s: %+v", previous.Version, successes)
					}
				}
				if current, err := first.Show(ctx, "beads/plan"); err != nil || !reflect.DeepEqual(current, previous) {
					t.Fatalf("current tail: %+v %v", current, err)
				}
				if got, err := first.ShowLink(ctx, "links/out"); err != nil || !reflect.DeepEqual(got, currentLink) {
					t.Fatalf("current Link: %+v %v", got, err)
				}
				if got, err := first.Show(ctx, "beads/target"); err != nil || !reflect.DeepEqual(got, target) {
					t.Fatalf("target changed: %+v %v", got, err)
				}
				var count int
				if err := first.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM graph_preview_versions WHERE path='beads/plan'").Scan(&count); err != nil || count != 3+len(successes) {
					t.Fatalf("versions=%d want=%d: %v", count, 3+len(successes), err)
				}
			})
		}
	}
}
