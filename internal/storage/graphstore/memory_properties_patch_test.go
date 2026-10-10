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

	"github.com/steveyegge/beads/internal/storage"
)

func memoryPropertiesOperation(t *testing.T, field, value string) []byte {
	t.Helper()
	data, err := json.Marshal([]map[string]any{{"op": "replace", "path": "/" + field, "value": value}})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func assertMemoryPropertiesTransition(t *testing.T, before Record, got MemoryMutationResult, want Properties, actor string, unconditional bool) {
	t.Helper()
	expected := before
	expected.Properties = want
	expected.Version, expected.Revision, expected.Attribution = got.Memory.Version, got.Memory.Revision, got.Memory.Attribution
	if !got.Changed || got.Memory.Version == before.Version || got.Memory.Version != got.Memory.Revision || !reflect.DeepEqual(expected, got.Memory) || got.Memory.Attribution.Actor != actor {
		t.Fatalf("incomplete transition: before=%+v got=%+v want=%+v", before, got, want)
	}
	if unconditional {
		assertReplacedMemory(t, got.Replaced, before)
	} else if got.Replaced != nil {
		t.Fatal("guarded patch disclosed unconditional predecessor")
	}
}

func TestMemoryPropertiesPatchLifecycle(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s, original, target, link := disclosureFixture(t, backend)
			retained := []Record{original}
			title := "  記憶\r\n"
			first, err := s.PatchMemoryProperties(ctx, MemoryPropertiesPatchRequest{Path: "beads/plan", Patch: memoryPropertiesOperation(t, "title", title), Actor: "editor", Unconditional: true})
			if err != nil {
				t.Fatal(err)
			}
			assertMemoryPropertiesTransition(t, original, first, Properties{Title: title, Body: original.Properties.Body}, "editor", true)
			// The fixture has unknown attribution; R14 must preserve that, not invent a principal.
			if original.Attribution.Status != "unknown" || original.Attribution.Actor != "" {
				t.Fatal("fixture attribution changed")
			}
			retained = append(retained, first.Memory)
			temporary := []byte(`[{"op":"replace","path":"","value":{"title":"root","body":"body","scratch":["a"]}},{"op":"add","path":"/scratch/-","value":"b"},{"op":"remove","path":"/scratch/0"},{"op":"remove","path":"/scratch"},{"op":"replace","path":"/body","value":"  雪\r\n\n"}]`)
			next, err := s.PatchMemoryProperties(ctx, MemoryPropertiesPatchRequest{Path: "beads/plan", Patch: temporary, Actor: "ordered", ExpectedRevision: first.Memory.Revision})
			if err != nil {
				t.Fatal(err)
			}
			assertMemoryPropertiesTransition(t, first.Memory, next, Properties{Title: "root", Body: "  雪\r\n\n"}, "ordered", false)
			retained = append(retained, next.Memory)
			before := workflowState(t, ctx, s)
			for _, patch := range [][]byte{[]byte(`[{"op":"replace","path":"/title","value":"root"}]`), []byte(`[{"op":"replace","path":"/title","value":"temporary"},{"op":"replace","path":"/title","value":"root"}]`)} {
				for _, unconditional := range []bool{false, true} {
					request := MemoryPropertiesPatchRequest{Path: "beads/plan", Patch: patch, Actor: "noop", Unconditional: unconditional}
					if !unconditional {
						request.ExpectedRevision = next.Memory.Revision
					}
					got, err := s.PatchMemoryProperties(ctx, request)
					if err != nil || got.Changed || got.Replaced != nil || !reflect.DeepEqual(got.Memory, next.Memory) {
						t.Fatalf("semantic no-op: %+v %v", got, err)
					}
				}
			}
			if !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
				t.Fatal("no-op wrote coordination/history/payload")
			}
			changedLink, err := s.UpdateLink(ctx, LinkUpdateRequest{Path: "links/out", Properties: map[string]any{"note": "accepted"}, ExpectedRevision: link.Link.Revision, ExpectedSourceRevision: next.Memory.Revision, Actor: "link-editor"})
			if err != nil {
				t.Fatal(err)
			}
			predecessor := changedLink.Source.(Record)
			retained = append(retained, predecessor)
			before = workflowState(t, ctx, s)
			for _, patch := range [][]byte{[]byte(`[{"op":"replace","path":"/title","value":"root"}]`), []byte(`[{"op":"remove","path":"/missing"}]`)} {
				got, err := s.PatchMemoryProperties(ctx, MemoryPropertiesPatchRequest{Path: "beads/plan", Patch: patch, ExpectedRevision: next.Memory.Revision})
				if !errors.Is(err, ErrConflict) || !reflect.ValueOf(got).IsZero() {
					t.Fatalf("owned guard must precede no-op/evaluation: %+v %v", got, err)
				}
			}
			if !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
				t.Fatal("stale guard changed state")
			}
			// Mutating caller bytes in the transaction hook cannot rewrite the parsed operation.
			captured := memoryPropertiesOperation(t, "body", "copied")
			s.afterWrite = func(stage string) error {
				if stage == "coordination" {
					for i := range captured {
						captured[i] = 'x'
					}
				}
				return nil
			}
			copied, err := s.PatchMemoryProperties(ctx, MemoryPropertiesPatchRequest{Path: "beads/plan", Patch: captured, Actor: "copier", Unconditional: true})
			s.afterWrite = nil
			if err != nil {
				t.Fatal(err)
			}
			assertMemoryPropertiesTransition(t, predecessor, copied, Properties{Title: "root", Body: "copied"}, "copier", true)
			assertOwnedLink(t, copied.Memory, changedLink.Link)
			retained = append(retained, copied.Memory)
			clear, err := s.PatchMemoryProperties(ctx, MemoryPropertiesPatchRequest{Path: "beads/plan", Patch: []byte(`[{"op":"replace","path":"","value":{"title":"","body":""}}]`), ExpectedRevision: copied.Memory.Revision})
			if err != nil {
				t.Fatal(err)
			}
			assertMemoryPropertiesTransition(t, copied.Memory, clear, Properties{}, "", false)
			retained = append(retained, clear.Memory)
			if got, err := s.Show(ctx, "beads/plan"); err != nil || !reflect.DeepEqual(got, clear.Memory) {
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
			var count int
			if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM graph_preview_versions WHERE path='beads/plan'").Scan(&count); err != nil || count != len(retained)+1 {
				t.Fatalf("retained count=%d want%d: %v", count, len(retained)+1, err)
			}
		})
	}
}

func TestMemoryPropertiesPatchRefusalAndRollback(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s, original, _, _ := disclosureFixture(t, backend)
			if _, err := s.CreateIssue(ctx, "beads/work", plainIssue("Work")); err != nil {
				t.Fatal(err)
			}
			before := workflowState(t, ctx, s)
			assertFailure := func(t *testing.T, got MemoryMutationResult, err, want error) {
				t.Helper()
				if !errors.Is(err, want) || !reflect.ValueOf(got).IsZero() {
					t.Fatalf("refusal: %+v %v want%v", got, err, want)
				}
				if !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
					t.Fatal("refusal changed full store state")
				}
			}
			request := MemoryPropertiesPatchRequest{Path: "beads/plan", Patch: memoryPropertiesOperation(t, "body", "changed"), Actor: "writer", Unconditional: true}
			for _, tc := range []struct{ name, patch string }{
				{"empty-operations", `[]`}, {"malformed", `[{`}, {"unsupported-op", `[{"op":"test","path":"/title","value":""}]`},
				{"missing-target", `[{"op":"replace","path":"/missing","value":"x"}]`},
				{"missing-title", `[{"op":"remove","path":"/title"}]`},
				{"missing-body", `[{"op":"remove","path":"/body"}]`},
				{"null-title", `[{"op":"replace","path":"/title","value":null}]`},
				{"null-body", `[{"op":"replace","path":"/body","value":null}]`},
				{"extra-member", `[{"op":"add","path":"/extra","value":"x"}]`},
				{"array-final", `[{"op":"replace","path":"","value":[]}]`},
				{"late-op", `[{"op":"replace","path":"/body","value":"early"},{"op":"remove","path":"/missing"}]`},
			} {
				t.Run(tc.name, func(t *testing.T) {
					r := request
					r.Patch = []byte(tc.patch)
					got, err := s.PatchMemoryProperties(ctx, r)
					assertFailure(t, got, err, storage.ErrValidation)
				})
			}
			for _, tc := range []struct {
				name   string
				change func(*MemoryPropertiesPatchRequest)
				want   error
			}{
				{"invalid-actor", func(r *MemoryPropertiesPatchRequest) { r.Actor = "\xff" }, storage.ErrValidation},
				{"invalid-utf8", func(r *MemoryPropertiesPatchRequest) { r.Patch = []byte{'[', '"', 0xff, '"', ']'} }, storage.ErrValidation},
				{"missing", func(r *MemoryPropertiesPatchRequest) { r.Path = "beads/missing" }, ErrNotFound},
				{"Issue-kind", func(r *MemoryPropertiesPatchRequest) { r.Path = "beads/work" }, ErrCapabilityUnavailable},
				{"both-guards", func(r *MemoryPropertiesPatchRequest) { r.ExpectedRevision = original.Revision }, storage.ErrValidation},
				{"neither-guard", func(r *MemoryPropertiesPatchRequest) { r.Unconditional = false }, storage.ErrValidation},
				{"oversized-input", func(r *MemoryPropertiesPatchRequest) { r.Patch = []byte(strings.Repeat(" ", (1<<20)+1)) }, ErrLimitExceeded},
				{"too-many-operations", func(r *MemoryPropertiesPatchRequest) {
					r.Patch = []byte("[" + strings.TrimSuffix(strings.Repeat(`{"op":"replace","path":"/body","value":"x"},`, 257), ",") + "]")
				}, ErrLimitExceeded},
				{"oversized-pointer", func(r *MemoryPropertiesPatchRequest) {
					r.Patch = []byte(`[{"op":"add","path":"/` + strings.Repeat("x", 4096) + `","value":"x"}]`)
				}, ErrLimitExceeded},
			} {
				t.Run(tc.name, func(t *testing.T) {
					r := request
					tc.change(&r)
					got, err := s.PatchMemoryProperties(ctx, r)
					assertFailure(t, got, err, tc.want)
				})
			}
			for _, stage := range []string{"coordination", "memory-payload", "source-catalog", "source-retained"} {
				t.Run("rollback-"+stage, func(t *testing.T) {
					fault := errors.New(stage)
					s.afterWrite = func(at string) error {
						if at == stage {
							return fault
						}
						return nil
					}
					got, err := s.PatchMemoryProperties(ctx, request)
					s.afterWrite = nil
					assertFailure(t, got, err, fault)
				})
			}
			t.Run("cancel-after-retention", func(t *testing.T) {
				canceled, cancel := context.WithCancel(ctx)
				defer cancel()
				s.afterWrite = func(stage string) error {
					if stage == "source-retained" {
						cancel()
						return canceled.Err()
					}
					return nil
				}
				got, err := s.PatchMemoryProperties(canceled, request)
				s.afterWrite = nil
				assertFailure(t, got, err, context.Canceled)
			})
			t.Run("authority", func(t *testing.T) {
				o := s.options
				s.options.Binding.AuthorityID = "ffffffffffffffffffffffffffffffff"
				got, err := s.PatchMemoryProperties(ctx, request)
				s.options = o
				assertFailure(t, got, err, ErrInvalidStore)
			})
		})
	}
}

func TestMemoryPropertiesPatchReadBudget(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s, original, target, _ := disclosureFixture(t, backend)
			// Two current copies of 7.5MiB fit; adding two 768KiB subject copies does not.
			if _, err := s.Create(ctx, CreateRequest{Path: "beads/filler", Body: strings.Repeat("f", PreviewCurrentReadByteLimit/2-(512<<10))}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Read(ctx, "beads/plan"); err != nil {
				t.Fatalf("fixture must start readable: %v", err)
			}
			before := workflowState(t, ctx, s)
			large := strings.Repeat("b", 768<<10)
			for _, unconditional := range []bool{false, true} {
				t.Run(fmt.Sprintf("unconditional-%t", unconditional), func(t *testing.T) {
					r := MemoryPropertiesPatchRequest{Path: "beads/plan", Patch: memoryPropertiesOperation(t, "body", large), Unconditional: unconditional}
					if !unconditional {
						r.ExpectedRevision = original.Revision
					}
					got, err := s.PatchMemoryProperties(ctx, r)
					if !errors.Is(err, ErrLimitExceeded) || !reflect.ValueOf(got).IsZero() || !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
						t.Fatalf("budget rollback: %+v %v", got, err)
					}
				})
			}
			// Content replacement also refuses growth and leaves all state intact.
			refused, err := s.PatchMemory(ctx, MemoryPatchRequest{Path: "beads/plan", Body: &large, ExpectedRevision: original.Revision})
			if !errors.Is(err, ErrLimitExceeded) || !reflect.ValueOf(refused).IsZero() || !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
				t.Fatalf("content budget rollback: %+v %v", refused, err)
			}
			// Seed canonical oversized state from before that writer guard existed.
			legacy := MemoryMutationResult{Memory: replaceLegacyFixtureMemory(t, ctx, s, "beads/plan", original, large)}
			if _, err := s.Read(ctx, "beads/plan"); !errors.Is(err, ErrLimitExceeded) {
				t.Fatalf("expected oversized current view: %v", err)
			}
			noop, err := s.PatchMemoryProperties(ctx, MemoryPropertiesPatchRequest{Path: "beads/plan", Patch: memoryPropertiesOperation(t, "body", large), ExpectedRevision: legacy.Memory.Revision})
			if err != nil || noop.Changed || !reflect.DeepEqual(noop.Memory, legacy.Memory) || noop.Replaced != nil {
				t.Fatalf("non-growing no-op: %+v %v", noop, err)
			}
			recovered, err := s.PatchMemoryProperties(ctx, MemoryPropertiesPatchRequest{Path: "beads/plan", Patch: memoryPropertiesOperation(t, "body", "small"), ExpectedRevision: legacy.Memory.Revision, Actor: "recovery"})
			if err != nil {
				t.Fatal(err)
			}
			assertMemoryPropertiesTransition(t, legacy.Memory, recovered, Properties{Title: original.Properties.Title, Body: "small"}, "recovery", false)
			if _, err := s.Read(ctx, "beads/plan"); err != nil {
				t.Fatalf("shrink did not restore readability: %v", err)
			}
			for _, record := range []Record{original, legacy.Memory, recovered.Memory} {
				if got, err := s.ReadVersion(ctx, "beads/plan", record.Version); err != nil || !reflect.DeepEqual(got, record) {
					t.Fatalf("exact budget state: %+v %v", got, err)
				}
			}
			if got, err := s.Show(ctx, "beads/target"); err != nil || !reflect.DeepEqual(got, target) {
				t.Fatalf("target changed: %+v %v", got, err)
			}
		})
	}
}

func TestMemoryPropertiesPatchConcurrentOwnedWriter(t *testing.T) {
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
					kind     string
					memory   Record
					link     LinkRecord
					replaced *ReplacedMemory
					changed  bool
					zero     bool
					err      error
				}
				results := make(chan outcome, 2)
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
				var writers sync.WaitGroup
				writers.Add(2)
				defer func() { cancel(); writers.Wait(); first.afterWrite = nil; second.afterWrite = nil }()
				patch := memoryPropertiesOperation(t, "body", "patched")
				go func() {
					defer writers.Done()
					r := MemoryPropertiesPatchRequest{Path: "beads/plan", Patch: patch, Actor: "patch-writer", Unconditional: unconditional}
					if !unconditional {
						r.ExpectedRevision = original.Revision
					}
					got, err := first.PatchMemoryProperties(ctx, r)
					results <- outcome{kind: "patch", memory: got.Memory, replaced: got.Replaced, changed: got.Changed, zero: reflect.ValueOf(got).IsZero(), err: err}
				}()
				go func() {
					defer writers.Done()
					r := LinkUpdateRequest{Path: "links/out", Properties: map[string]any{"note": "link-writer"}, Actor: "link-writer", Unconditional: unconditional, UnconditionalSource: unconditional}
					if !unconditional {
						r.ExpectedRevision = link.Link.Revision
						r.ExpectedSourceRevision = original.Revision
					}
					got, err := second.UpdateLink(ctx, r)
					memory, _ := got.Source.(Record)
					results <- outcome{kind: "link", memory: memory, link: got.Link, replaced: got.ReplacedSource, changed: got.Changed, zero: reflect.ValueOf(got).IsZero(), err: err}
				}()
				if backend == "server" {
					for range 2 {
						select {
						case <-reached:
						case got := <-results:
							t.Fatalf("writer failed before actual overlap: %+v", got)
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
								t.Fatal("accepted writer did not report changed")
							}
							successes = append(successes, got)
						} else if errors.Is(got.err, ErrConflict) && !errors.Is(got.err, ErrOutcomeUnknown) && got.zero {
							conflicts++
						} else {
							t.Fatalf("unexpected result/disclosure: %+v", got)
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
					t.Fatalf("successes=%d conflicts=%d", len(successes), conflicts)
				}
				// Do not assume completion order equals commit order; unconditional receipts
				// form an observable predecessor chain even under the embedded pool's serial order.
				previous, currentLink := original, link.Link
				used := map[string]bool{}
				for range len(successes) {
					found := false
					for _, got := range successes {
						if used[got.kind] {
							continue
						}
						if unconditional && (got.replaced == nil || got.replaced.Version != previous.Version) {
							continue
						}
						if unconditional {
							assertReplacedMemory(t, got.replaced, previous)
						} else if got.replaced != nil {
							t.Fatal("guarded winner disclosed predecessor")
						}
						want := previous.Properties
						wantOwned := previous.Owned
						if got.kind == "patch" {
							want.Body = "patched"
						} else {
							currentLink = got.link
							assertOwnedLink(t, got.memory, currentLink)
							wantOwned = got.memory.Owned
							expectedLink := link.Link
							expectedLink.Properties = map[string]any{"note": "link-writer"}
							expectedLink.Version, expectedLink.Revision, expectedLink.Attribution = currentLink.Version, currentLink.Revision, currentLink.Attribution
							if !reflect.DeepEqual(currentLink, expectedLink) || currentLink.Attribution.Actor != "link-writer" || currentLink.Version != currentLink.Revision || currentLink.Version == link.Link.Version {
								t.Fatal("incomplete Link winner")
							}
						}
						expected := previous
						expected.Properties = want
						expected.Owned = wantOwned
						expected.Version, expected.Revision, expected.Attribution = got.memory.Version, got.memory.Revision, got.memory.Attribution
						if !reflect.DeepEqual(got.memory, expected) || got.memory.Version == previous.Version || got.memory.Version != got.memory.Revision || got.memory.Attribution.Actor != got.kind+"-writer" {
							t.Fatalf("incomplete winning state: %+v", got)
						}
						if saved, err := first.ReadVersion(ctx, "beads/plan", got.memory.Version); err != nil || !reflect.DeepEqual(saved, got.memory) {
							t.Fatalf("winning retained state: %+v %v", saved, err)
						}
						used[got.kind] = true
						previous = got.memory
						found = true
						break
					}
					if !found {
						t.Fatalf("no successor to %s: %+v", previous.Version, successes)
					}
				}
				if got, err := first.Show(ctx, "beads/plan"); err != nil || !reflect.DeepEqual(got, previous) {
					t.Fatalf("winning current: %+v %v", got, err)
				}
				if got, err := first.ShowLink(ctx, "links/out"); err != nil || !reflect.DeepEqual(got, currentLink) {
					t.Fatalf("winning Link: %+v %v", got, err)
				}
				if got, err := first.Show(ctx, "beads/target"); err != nil || !reflect.DeepEqual(got, target) {
					t.Fatalf("target: %+v %v", got, err)
				}
				if got, err := first.ReadVersion(ctx, "beads/plan", original.Version); err != nil || !reflect.DeepEqual(got, original) {
					t.Fatalf("old owned snapshot: %+v %v", got, err)
				}
				var count int
				if err := first.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM graph_preview_versions WHERE path='beads/plan'").Scan(&count); err != nil || count != 2+len(successes) {
					t.Fatalf("accepted versions=%d want%d: %v", count, 2+len(successes), err)
				}
				next, err := first.PatchMemoryProperties(ctx, MemoryPropertiesPatchRequest{Path: "beads/plan", Patch: memoryPropertiesOperation(t, "title", "after race"), Actor: "after", Unconditional: true})
				if err != nil {
					t.Fatal(err)
				}
				want := previous.Properties
				want.Title = "after race"
				assertMemoryPropertiesTransition(t, previous, next, want, "after", true)
			})
		}
	}
}

func TestMemoryPropertiesPatchLostCommitResponse(t *testing.T) {
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
	got, err := s.PatchMemoryProperties(ctx, MemoryPropertiesPatchRequest{Path: "beads/plan", Patch: memoryPropertiesOperation(t, "body", "uncertain"), Actor: "uncertain", Unconditional: true})
	if !errors.Is(err, ErrOutcomeUnknown) || errors.Is(err, ErrConflict) || !reflect.ValueOf(got).IsZero() {
		t.Fatalf("lost COMMIT must publish no receipt/disclosure: %+v %v", got, err)
	}
	select {
	case packet := <-observed:
		if len(packet) == 0 || packet[0] != 0 {
			t.Fatalf("not a committed server response: %x", packet)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	current, err := direct.Show(ctx, "beads/plan")
	if err != nil {
		t.Fatal(err)
	}
	expected := original
	expected.Properties.Body = "uncertain"
	expected.Version, expected.Revision, expected.Attribution = current.Version, current.Revision, current.Attribution
	if !reflect.DeepEqual(current, expected) || current.Version == original.Version || current.Attribution.Actor != "uncertain" {
		t.Fatalf("incomplete witnessed commit: %+v", current)
	}
	for _, record := range []Record{original, current} {
		if got, err := direct.ReadVersion(ctx, "beads/plan", record.Version); err != nil || !reflect.DeepEqual(got, record) {
			t.Fatalf("saved commit state: %+v %v", got, err)
		}
	}
	if got, err := direct.Show(ctx, "beads/target"); err != nil || !reflect.DeepEqual(got, target) {
		t.Fatalf("target: %+v %v", got, err)
	}
	if got, err := direct.ShowLink(ctx, "links/out"); err != nil || !reflect.DeepEqual(got, link.Link) {
		t.Fatalf("Link: %+v %v", got, err)
	}
	var count int
	if err := direct.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM graph_preview_versions WHERE path='beads/plan'").Scan(&count); err != nil || count != 3 {
		t.Fatalf("single commit, no replay: count%d %v", count, err)
	}
}

// Legacy deleted Memories retain their final live head. The current reader
// charges those heads even though their current payloads are absent.
func propertiesPatchDeletedFiller(t *testing.T, ctx context.Context, s *Store) []Record {
	t.Helper()
	var retained []Record
	for _, path := range []string{"beads/deleted-filler-a", "beads/deleted-filler-b"} {
		memory := seedUnreadableMemory(t, ctx, s, path, strings.Repeat("f", PreviewCurrentReadByteLimit/2-(512<<10)))
		deleted, err := s.DeleteMemory(ctx, MemoryDeleteRequest{Path: path, ExpectedRevision: memory.Revision})
		if err != nil || !deleted.Deleted || !reflect.DeepEqual(deleted.Memory, memory) {
			t.Fatalf("legacy filler deletion: deleted=%t err=%v", deleted.Deleted, err)
		}
		retained = append(retained, memory)
	}
	return retained
}

func TestMemoryPropertiesPatchDeletedBudget(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s, original, _, _ := disclosureFixture(t, backend)
			deleted := propertiesPatchDeletedFiller(t, ctx, s)
			if _, err := s.Read(ctx, "beads/plan"); err != nil {
				t.Fatalf("deleted filler fixture unreadable: %v", err)
			}
			before := workflowState(t, ctx, s)
			for _, unconditional := range []bool{false, true} {
				r := MemoryPropertiesPatchRequest{Path: "beads/plan", Patch: memoryPropertiesOperation(t, "body", strings.Repeat("b", 768<<10)), Unconditional: unconditional}
				if !unconditional {
					r.ExpectedRevision = original.Revision
				}
				got, err := s.PatchMemoryProperties(ctx, r)
				if !errors.Is(err, ErrLimitExceeded) || !reflect.ValueOf(got).IsZero() || !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
					t.Fatalf("deleted-head budget rollback: changed=%t err=%v", got.Changed, err)
				}
			}
			for i, path := range []string{"beads/deleted-filler-a", "beads/deleted-filler-b"} {
				got, err := s.PatchMemoryProperties(ctx, MemoryPropertiesPatchRequest{Path: path, Patch: memoryPropertiesOperation(t, "body", "resurrect"), ExpectedRevision: deleted[i].Revision})
				if !errors.Is(err, ErrGone) || !reflect.ValueOf(got).IsZero() {
					t.Fatalf("deleted subject patched: changed=%t err=%v", got.Changed, err)
				}
				retained, err := s.ReadVersion(ctx, path, deleted[i].Version)
				if err != nil || !reflect.DeepEqual(retained, deleted[i]) {
					t.Fatalf("deleted retained head changed: %v", err)
				}
			}
			if !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
				t.Fatal("deleted subject refusal changed state")
			}
			if got, err := s.Read(ctx, "beads/plan"); err != nil || !reflect.DeepEqual(got, original) {
				t.Fatalf("budget refusal changed current Memory: %v", err)
			}
		})
	}
}
