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
)

func disclosureFixture(t *testing.T, backend string) (context.Context, Options, *Store, Record, Record, LinkMutationResult) {
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
	memory, err := s.Create(ctx, CreateRequest{Path: "beads/plan", Body: "original"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := s.Create(ctx, CreateRequest{Path: "beads/target", Body: "target", Actor: "target-author"})
	if err != nil {
		t.Fatal(err)
	}
	link, err := s.AddInformationalLink(ctx, LinkCreateRequest{Path: "links/out", SourcePath: "beads/plan", TargetPath: "beads/target", ExpectedSourceRevision: memory.Revision})
	if err != nil {
		t.Fatal(err)
	}
	return ctx, o, s, link.Source.(Record), target, link
}

func assertReplacedMemory(t *testing.T, got *ReplacedMemory, want Record) {
	t.Helper()
	expected := &ReplacedMemory{ID: want.ID, Version: want.Version, Attribution: want.Attribution}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("replacement disclosure=%+v want=%+v", got, expected)
	}
}

func TestOverwriteDisclosureLifecycle(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s, original, target, link := disclosureFixture(t, backend)
			if original.Attribution.Status != "unknown" || original.Attribution.Actor != "" {
				t.Fatalf("fixture not unknown: %+v", original.Attribution)
			}
			edit, err := s.UpdateMemory(ctx, MemoryUpdateRequest{Path: "beads/plan", Properties: Properties{Body: "edited"}, Actor: "editor", Unconditional: true})
			if err != nil {
				t.Fatal(err)
			}
			assertReplacedMemory(t, edit.Replaced, original)
			before := workflowState(t, ctx, s)
			noop, err := s.UpdateMemory(ctx, MemoryUpdateRequest{Path: "beads/plan", Properties: edit.Memory.Properties, Actor: "other", Unconditional: true})
			if err != nil || noop.Changed || noop.Replaced != nil {
				t.Fatalf("noop: %+v %v", noop, err)
			}
			if !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
				t.Fatal("noop changed state")
			}
			guarded, err := s.UpdateMemory(ctx, MemoryUpdateRequest{Path: "beads/plan", Properties: Properties{Body: "guarded"}, ExpectedRevision: edit.Memory.Revision, Actor: "guarded-editor"})
			if err != nil || guarded.Replaced != nil {
				t.Fatalf("guarded: %+v %v", guarded, err)
			}
			added, err := s.AddInformationalLink(ctx, LinkCreateRequest{Path: "links/second", SourcePath: "beads/plan", TargetPath: "beads/target", UnconditionalSource: true, Actor: "link-author"})
			if err != nil {
				t.Fatal(err)
			}
			assertReplacedMemory(t, added.ReplacedSource, guarded.Memory)
			updated, err := s.UpdateLink(ctx, LinkUpdateRequest{Path: "links/out", ExpectedRevision: link.Link.Revision, UnconditionalSource: true, Properties: map[string]any{"note": "new"}, Actor: "link-editor"})
			if err != nil {
				t.Fatal(err)
			}
			assertReplacedMemory(t, updated.ReplacedSource, added.Source.(Record))
			before = workflowState(t, ctx, s)
			linkNoop, err := s.UpdateLink(ctx, LinkUpdateRequest{Path: "links/out", Unconditional: true, UnconditionalSource: true, Properties: map[string]any{"note": "new"}, Actor: "other"})
			if err != nil || linkNoop.Changed || linkNoop.ReplacedSource != nil {
				t.Fatalf("link noop: %+v %v", linkNoop, err)
			}
			if !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
				t.Fatal("link noop changed state")
			}
			removed, err := s.Unlink(ctx, LinkDeleteRequest{Path: "links/out", ExpectedRevision: updated.Link.Revision, UnconditionalSource: true, Actor: "remover"})
			if err != nil {
				t.Fatal(err)
			}
			assertReplacedMemory(t, removed.ReplacedSource, updated.Source.(Record))
			if got, err := s.Show(ctx, "beads/target"); err != nil || !reflect.DeepEqual(got, target) {
				t.Fatalf("target changed: %+v %v", got, err)
			}
			for _, snapshot := range []Record{original, edit.Memory, guarded.Memory, added.Source.(Record), updated.Source.(Record)} {
				if got, err := s.ReadVersion(ctx, "beads/plan", snapshot.Version); err != nil || !reflect.DeepEqual(got, snapshot) {
					t.Fatalf("old snapshot changed: %+v %v", got, err)
				}
			}
		})
	}
}

// Each operation reaches its final retained-source stage, then fails. No
// predecessor disclosure is a success receipt until COMMIT is known to succeed.
func TestOverwriteDisclosureRollback(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s, _, _, _ := disclosureFixture(t, backend)
			before := workflowState(t, ctx, s)
			fault := errors.New("reject retained mutation")
			operations := map[string]func(context.Context, *Store) (any, error){
				"memory": func(ctx context.Context, s *Store) (any, error) {
					return s.UpdateMemory(ctx, MemoryUpdateRequest{Path: "beads/plan", Properties: Properties{Body: "new"}, Unconditional: true})
				},
				"create": func(ctx context.Context, s *Store) (any, error) {
					return s.AddInformationalLink(ctx, LinkCreateRequest{Path: "links/new", SourcePath: "beads/plan", TargetPath: "beads/target", UnconditionalSource: true})
				},
				"update": func(ctx context.Context, s *Store) (any, error) {
					return s.UpdateLink(ctx, LinkUpdateRequest{Path: "links/out", Unconditional: true, UnconditionalSource: true, Properties: map[string]any{"note": "new"}})
				},
				"unlink": func(ctx context.Context, s *Store) (any, error) {
					return s.Unlink(ctx, LinkDeleteRequest{Path: "links/out", Unconditional: true, UnconditionalSource: true})
				},
			}
			for _, name := range []string{"memory", "create", "update", "unlink"} {
				t.Run(name, func(t *testing.T) {
					for _, cancelled := range []bool{false, true} {
						callCtx, cancel := context.WithCancel(ctx)
						s.afterWrite = func(stage string) error {
							if stage == "source-retained" {
								if cancelled {
									cancel()
									return callCtx.Err()
								}
								return fault
							}
							return nil
						}
						got, err := operations[name](callCtx, s)
						s.afterWrite = nil
						cancel()
						wantErr := fault
						if cancelled {
							wantErr = context.Canceled
						}
						if !errors.Is(err, wantErr) || !reflect.ValueOf(got).IsZero() {
							t.Fatalf("cancelled=%v result=%+v err=%v", cancelled, got, err)
						}
						if !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
							t.Fatalf("cancelled=%v leaked state", cancelled)
						}
					}
				})
			}
		})
	}
}

func TestOverwriteDisclosureConcurrentPredecessor(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			baseCtx, o, first, original, target, _ := disclosureFixture(t, backend)
			ctx, cancel := context.WithCancel(baseCtx)
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
				memory   Record
				replaced *ReplacedMemory
				err      error
				zero     bool
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
				got, err := first.UpdateMemory(ctx, MemoryUpdateRequest{Path: "beads/plan", Properties: Properties{Body: "writer-one"}, Actor: "one", Unconditional: true})
				results <- outcome{got.Memory, got.Replaced, err, reflect.ValueOf(got).IsZero()}
			}()
			go func() {
				defer writers.Done()
				got, err := second.UpdateLink(ctx, LinkUpdateRequest{Path: "links/out", Properties: map[string]any{"note": "writer-two"}, Actor: "two", Unconditional: true, UnconditionalSource: true})
				memory, _ := got.Source.(Record)
				results <- outcome{memory, got.ReplacedSource, err, reflect.ValueOf(got).IsZero()}
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
						t.Fatalf("writer: %+v", got)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			writers.Wait()
			first.afterWrite, second.afterWrite = nil, nil
			if backend == "server" && (len(successes) != 1 || conflicts != 1) {
				t.Fatalf("successes=%d conflicts=%d", len(successes), conflicts)
			}
			if backend == "embedded" && (len(successes) != 2 || conflicts != 0) {
				t.Fatalf("serialized successes=%d conflicts=%d", len(successes), conflicts)
			}
			// Receipt completion order is not assumed. Follow predecessor identity from
			// the original to the committed tail, validating exact saved state at each step.
			previous := original
			for range len(successes) {
				found := false
				for _, got := range successes {
					if got.replaced != nil && got.replaced.Version == previous.Version {
						assertReplacedMemory(t, got.replaced, previous)
						if old, err := first.ReadVersion(ctx, "beads/plan", previous.Version); err != nil || !reflect.DeepEqual(old, previous) {
							t.Fatalf("wrong retained predecessor: %+v %v", old, err)
						}
						previous = got.memory
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("missing receipt successor for %s: %+v", previous.Version, successes)
				}
			}
			current, err := first.Show(ctx, "beads/plan")
			if err != nil || !reflect.DeepEqual(current, previous) {
				t.Fatalf("wrong tail: %+v %v", current, err)
			}
			// A later caller must disclose the accepted winner, never the old race start.
			final, err := first.UpdateMemory(ctx, MemoryUpdateRequest{Path: "beads/plan", Properties: Properties{Body: "later"}, Unconditional: true, Actor: "later"})
			if err != nil {
				t.Fatal(err)
			}
			assertReplacedMemory(t, final.Replaced, current)
			if got, err := first.Show(ctx, "beads/target"); err != nil || !reflect.DeepEqual(got, target) {
				t.Fatalf("race changed target: %+v %v", got, err)
			}
		})
	}
}

func TestOverwriteDisclosureLostCommitResponse(t *testing.T) {
	for _, name := range []string{"memory", "create", "update", "unlink"} {
		t.Run(name, func(t *testing.T) {
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
			var got any
			switch name {
			case "memory":
				got, err = s.UpdateMemory(ctx, MemoryUpdateRequest{Path: "beads/plan", Properties: Properties{Body: "uncertain"}, Unconditional: true})
			case "create":
				got, err = s.AddInformationalLink(ctx, LinkCreateRequest{Path: "links/new", SourcePath: "beads/plan", TargetPath: "beads/target", UnconditionalSource: true})
			case "update":
				got, err = s.UpdateLink(ctx, LinkUpdateRequest{Path: "links/out", Properties: map[string]any{"note": "uncertain"}, Unconditional: true, UnconditionalSource: true})
			case "unlink":
				got, err = s.Unlink(ctx, LinkDeleteRequest{Path: "links/out", Unconditional: true, UnconditionalSource: true})
			}
			if !errors.Is(err, ErrOutcomeUnknown) || errors.Is(err, ErrConflict) || !reflect.ValueOf(got).IsZero() {
				t.Fatalf("unknown result=%+v err=%v", got, err)
			}
			select {
			case packet := <-observed:
				if len(packet) == 0 || packet[0] != 0 {
					t.Fatalf("no server commit witness: %x", packet)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			// Only the fault harness knows this outcome committed. The caller received no
			// predecessor receipt, and no retry occurs here or in the mutation itself.
			current, err := direct.Show(ctx, "beads/plan")
			if err != nil || current.Version == original.Version {
				t.Fatalf("witnessed commit missing: %+v %v", current, err)
			}
			if old, err := direct.ReadVersion(ctx, "beads/plan", original.Version); err != nil || !reflect.DeepEqual(old, original) {
				t.Fatalf("old snapshot changed: %+v %v", old, err)
			}
			if got, err := direct.Show(ctx, "beads/target"); err != nil || !reflect.DeepEqual(got, target) {
				t.Fatalf("target changed: %+v %v", got, err)
			}
		})
	}
}
