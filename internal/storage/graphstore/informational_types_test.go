//go:build cgo

package graphstore

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	publicops "github.com/steveyegge/beads/issueops"
)

func TestExampleInformationalTypeLifecycle(t *testing.T) {
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
			target, err := s.Create(ctx, CreateRequest{Path: "beads/context", Body: "Context"})
			if err != nil {
				t.Fatal(err)
			}
			for _, typ := range []string{ExampleFollowsTypeURL(s.ScopeURL()), ExampleCitesTypeURL(s.ScopeURL())} {
				name := strings.TrimPrefix(typ, s.ScopeURL()+"types/")
				t.Run(name, func(t *testing.T) {
					path := "beads/" + name
					original, err := s.Create(ctx, CreateRequest{Path: path, Body: "Code flow policy"})
					if err != nil {
						t.Fatal(err)
					}
					linkPath := "links/" + name
					request := LinkCreateRequest{Path: linkPath, SourcePath: path, TargetPath: "beads/work", TypeURL: typ, ExpectedSourceRevision: original.Revision, Properties: map[string]any{"note": "before"}}
					before := workflowState(t, ctx, s)
					for _, stage := range []string{"coordination", "link-catalog", "link-payload", "link-retained", "source-catalog", "source-retained"} {
						injected := errors.New("example rollback")
						s.afterWrite = func(at string) error {
							if at == stage {
								return injected
							}
							return nil
						}
						_, err := s.AddInformationalLink(ctx, request)
						s.afterWrite = nil
						if !errors.Is(err, injected) || !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
							t.Fatalf("%s leaked a failed typed Link: %v", stage, err)
						}
					}
					added, err := s.AddInformationalLink(ctx, request)
					if err != nil {
						t.Fatal(err)
					}
					owner := added.Source.(Record)
					if added.Link.Type != typ || owner.Type != original.Type || owner.Revision == original.Revision {
						t.Fatalf("Type or ownership differs: %+v", added)
					}
					assertOwnedLink(t, owner, added.Link)
					edited, err := s.UpdateLink(ctx, LinkUpdateRequest{Path: linkPath, ExpectedRevision: added.Link.Revision, ExpectedSourceRevision: owner.Revision, Properties: map[string]any{"note": "after"}})
					if err != nil || edited.Link.Type != typ || !edited.Changed {
						t.Fatalf("typed update: %+v %v", edited, err)
					}
					editedOwner := edited.Source.(Record)
					assertOwnedLink(t, editedOwner, edited.Link)
					before = workflowState(t, ctx, s)
					noop := LinkUpdateRequest{Path: linkPath, ExpectedRevision: edited.Link.Revision, ExpectedSourceRevision: editedOwner.Revision, Properties: edited.Link.Properties}
					got, err := s.UpdateLink(ctx, noop)
					if err != nil || got.Changed || !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
						t.Fatalf("typed no-op mutated: %v", err)
					}
					noop.ExpectedSourceRevision = owner.Revision
					if _, err := s.UpdateLink(ctx, noop); !errors.Is(err, ErrConflict) || !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
						t.Fatalf("stale typed no-op accepted/mutated: %v", err)
					}
					links, err := s.ListLinks(ctx, LinksRequest{BeadPath: path, Direction: "out", TypeURL: typ})
					if err != nil || len(links) != 1 || !reflect.DeepEqual(links[0], edited.Link) {
						t.Fatalf("typed incident inventory: %+v %v", links, err)
					}
					removed, err := s.Unlink(ctx, LinkDeleteRequest{SourcePath: path, TargetPath: "beads/work", TypeURL: typ, ExpectedRevision: edited.Link.Revision, ExpectedSourceRevision: editedOwner.Revision})
					if err != nil || removed.Link.Type != typ || len(removed.Source.(Record).Owned) != 0 {
						t.Fatalf("typed unlink: %+v %v", removed, err)
					}
					for _, want := range []Record{original, owner, editedOwner} {
						got, err := s.ReadVersion(ctx, path, want.Version)
						if err != nil || !reflect.DeepEqual(got, want) {
							t.Fatalf("old Memory snapshot changed: %v", err)
						}
					}
					for _, want := range []LinkRecord{added.Link, edited.Link} {
						got, err := s.ReadVersion(ctx, linkPath, want.Version)
						if err != nil || !reflect.DeepEqual(got, want) {
							t.Fatalf("old typed Link snapshot changed: %v", err)
						}
					}
					if _, err := s.ReadVersion(ctx, linkPath, removed.Link.Version); !errors.Is(err, ErrGone) {
						t.Fatalf("typed tombstone: %v", err)
					}
					request.ExpectedSourceRevision = removed.Source.(Record).Revision
					if _, err := s.AddInformationalLink(ctx, request); !errors.Is(err, ErrAlreadyExists) {
						t.Fatalf("deleted identity reused: %v", err)
					}
					// Memory/Memory and Issue/Memory and Issue/Issue use the same descriptor
					// without silently converting to Related or changing Issue ownership.
					request.Path, request.TargetPath = linkPath+"-memory", "beads/context"
					if _, err := s.AddInformationalLink(ctx, request); err != nil {
						t.Fatal(err)
					}
					for i, endpoint := range []string{"beads/context", "beads/work"} {
						unowned, err := s.AddInformationalLink(ctx, LinkCreateRequest{Path: linkPath + []string{"-issue-memory", "-issue-issue"}[i], SourcePath: "beads/work", TargetPath: endpoint, TypeURL: typ})
						if err != nil || unowned.Link.Type != typ || !reflect.DeepEqual(unowned.Source, issue) {
							t.Fatalf("unowned example changed Issue: %+v %v", unowned, err)
						}
					}
				})
			}
			if got, err := s.Show(ctx, "beads/context"); err != nil || !reflect.DeepEqual(got, target) {
				t.Fatalf("incoming examples changed target: %v", err)
			}
			mixed := target
			for i, typ := range []string{ExampleFollowsTypeURL(s.ScopeURL()), ExampleCitesTypeURL(s.ScopeURL())} {
				added, err := s.AddInformationalLink(ctx, LinkCreateRequest{Path: []string{"links/mixed-follows", "links/mixed-cites"}[i], SourcePath: "beads/context", TargetPath: "beads/work", TypeURL: typ, ExpectedSourceRevision: mixed.Revision})
				if err != nil {
					t.Fatal(err)
				}
				prior := mixed
				mixed = added.Source.(Record)
				if got, err := s.ReadVersion(ctx, "beads/context", prior.Version); err != nil || !reflect.DeepEqual(got, prior) {
					t.Fatalf("mixed ownership rewrote earlier snapshot: %v", err)
				}
			}
			ownedTypes := map[string]bool{}
			for _, raw := range mixed.Owned {
				var link LinkRecord
				if err := json.Unmarshal(raw, &link); err != nil {
					t.Fatal(err)
				}
				ownedTypes[link.Type] = true
			}
			if len(mixed.Owned) != 2 || !ownedTypes[ExampleFollowsTypeURL(s.ScopeURL())] || !ownedTypes[ExampleCitesTypeURL(s.ScopeURL())] {
				t.Fatalf("mixed-Type ownership collapsed: %+v", mixed)
			}
			if got, err := s.ReadVersion(ctx, "beads/context", mixed.Version); err != nil || !reflect.DeepEqual(got, mixed) {
				t.Fatalf("mixed owned version: %v", err)
			}
			assertReadyIDs(t, ctx, s, issue.ID)
			if got, err := s.ListIssues(ctx, publicops.ListRequest{}); err != nil || len(got.Items) != 1 || !reflect.DeepEqual(got.Items[0], issue) {
				t.Fatalf("Issue query with examples: %+v %v", got, err)
			}
			inventory, err := s.CurrentSnapshot(ctx)
			if err != nil || len(inventory.Types) != 6 {
				t.Fatalf("six-Type inventory: %+v %v", inventory.Types, err)
			}
			for _, record := range inventory.Records {
				if link, ok := record.(LinkRecord); ok && link.Type == RelatedTypeURL(s.ScopeURL()) {
					t.Fatal("example silently became Related")
				}
			}
		})
	}
}

func TestExampleTypesLegacyInstallation(t *testing.T) {
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
			// Removing unused example rows recreates precisely the prior four-row
			// installation; its schema, original descriptors and record format match.
			if _, err := s.db.ExecContext(ctx, `DELETE FROM graph_preview_types WHERE name IN ('example-follows','example-cites')`); err != nil {
				t.Fatal(err)
			}
			source, err := s.Create(ctx, CreateRequest{Path: "beads/source", Body: "Original policy"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.CreateIssue(ctx, "beads/work", plainIssue("Work")); err != nil {
				t.Fatal(err)
			}
			legacy, err := s.AddInformationalLink(ctx, LinkCreateRequest{Path: "links/legacy", SourcePath: "beads/source", TargetPath: "beads/work", ExpectedSourceRevision: source.Revision})
			if err != nil || legacy.Link.Type != RelatedTypeURL(s.ScopeURL()) {
				t.Fatalf("legacy default: %+v %v", legacy, err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = OpenExisting(ctx, o)
			if err != nil {
				t.Fatal(err)
			}
			before := workflowState(t, ctx, s)
			for _, typ := range []string{ExampleFollowsTypeURL(s.ScopeURL()), ExampleCitesTypeURL(s.ScopeURL())} {
				if _, err := s.ReadType(ctx, strings.TrimPrefix(typ, s.ScopeURL())); !errors.Is(err, ErrNotFound) {
					t.Fatalf("fabricated uninstalled Type: %v", err)
				}
				if _, err := s.AddInformationalLink(ctx, LinkCreateRequest{SourcePath: "beads/source", TargetPath: "beads/work", TypeURL: typ, UnconditionalSource: true}); !errors.Is(err, ErrCapabilityUnavailable) {
					t.Fatalf("created uninstalled Type: %v", err)
				}
				if _, err := s.ListLinks(ctx, LinksRequest{BeadPath: "beads/source", TypeURL: typ}); !errors.Is(err, ErrCapabilityUnavailable) {
					t.Fatalf("filtered uninstalled Type: %v", err)
				}
				if _, err := s.Unlink(ctx, LinkDeleteRequest{SourcePath: "beads/source", TargetPath: "beads/work", TypeURL: typ, Unconditional: true, UnconditionalSource: true}); !errors.Is(err, ErrCapabilityUnavailable) {
					t.Fatalf("selected uninstalled Type: %v", err)
				}
			}
			current, err := s.CurrentSnapshot(ctx)
			if err != nil || len(current.Types) != 4 {
				t.Fatalf("legacy inventory upgraded: %+v %v", current.Types, err)
			}
			for _, descriptor := range current.Types {
				var want []byte
				for _, definition := range previewTypeDefinitions()[:4] {
					expected, err := definition.build(s.ScopeURL())
					if err != nil {
						t.Fatal(err)
					}
					if expected.ID() == descriptor.ID() {
						want = expected.CanonicalJSON()
					}
				}
				if string(want) != string(descriptor.CanonicalJSON()) {
					t.Fatal("original descriptor changed")
				}
			}
			for _, want := range []Record{source, legacy.Source.(Record)} {
				got, err := s.ReadVersion(ctx, "beads/source", want.Version)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("legacy retained snapshot changed: %v", err)
				}
			}
			if !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
				t.Fatal("legacy reads/refusals changed state")
			}
		})
	}
}

func TestExampleTypesRefuseMissingAndCorruptInstallation(t *testing.T) {
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
			original, err := s.Create(ctx, CreateRequest{Path: "beads/source", Body: "Policy"})
			if err != nil {
				t.Fatal(err)
			}
			linked, err := s.AddInformationalLink(ctx, LinkCreateRequest{Path: "links/follows", SourcePath: "beads/source", TargetPath: "beads/source", TypeURL: ExampleFollowsTypeURL(s.ScopeURL()), ExpectedSourceRevision: original.Revision})
			if err != nil {
				t.Fatal(err)
			}
			for _, statement := range []string{
				`DELETE FROM graph_preview_types WHERE name='example-cites'`,
				`UPDATE graph_preview_types SET fingerprint='bad' WHERE name='example-follows'`,
				`DELETE FROM graph_preview_types WHERE name IN ('example-follows','example-cites')`,
			} {
				if _, err := s.db.ExecContext(ctx, statement); err != nil {
					t.Fatal(err)
				}
				before := workflowState(t, ctx, s)
				if _, err := s.ShowLink(ctx, "links/follows"); !errors.Is(err, ErrInvalidStore) {
					t.Fatalf("current accepted absent/corrupt Type: %v", err)
				}
				if _, err := s.ReadVersion(ctx, "links/follows", linked.Link.Version); !errors.Is(err, ErrInvalidStore) {
					t.Fatalf("retained Link accepted absent/corrupt Type: %v", err)
				}
				if _, err := s.ReadVersion(ctx, "beads/source", linked.Source.(Record).Version); !errors.Is(err, ErrInvalidStore) {
					t.Fatalf("retained owner accepted absent/corrupt Type: %v", err)
				}
				if _, err := s.CurrentSnapshot(ctx); !errors.Is(err, ErrInvalidStore) {
					t.Fatalf("inventory hid absent/corrupt Type: %v", err)
				}
				if _, err := s.ListIssues(ctx, publicops.ListRequest{}); !errors.Is(err, ErrInvalidStore) {
					t.Fatalf("empty Issue query hid invalid Link: %v", err)
				}
				if !reflect.DeepEqual(before, workflowState(t, ctx, s)) {
					t.Fatal("invalid Type read changed state")
				}
				for _, definition := range previewTypeDefinitions()[4:] {
					descriptor, err := definition.build(s.ScopeURL())
					if err != nil {
						t.Fatal(err)
					}
					if _, err := s.db.ExecContext(ctx, `REPLACE INTO graph_preview_types(name,descriptor,fingerprint) VALUES(?,?,?)`, definition.name, descriptor.CanonicalJSON(), descriptor.Fingerprint()); err != nil {
						t.Fatal(err)
					}
				}
			}
		})
	}
}
