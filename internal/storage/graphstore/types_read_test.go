//go:build cgo

package graphstore

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	graph "github.com/steveyegge/beads/graphops"
)

func TestReadInstalledTypes(t *testing.T) {
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
			if s.ScopeURL() != o.Binding.ScopeURL {
				t.Fatal("Scope identity differs from workspace binding")
			}
			var writerBefore, writerAfter string
			if err := s.db.QueryRowContext(ctx, `SELECT writer_token FROM graph_preview_scope WHERE singleton=1`).Scan(&writerBefore); err != nil {
				t.Fatal(err)
			}
			for name, id := range map[string]string{"memory": MemoryTypeURL(o.Binding.ScopeURL), "issue": IssueTypeURL(o.Binding.ScopeURL), "dependency": DependencyTypeURL(o.Binding.ScopeURL), "related": RelatedTypeURL(o.Binding.ScopeURL), "example-follows": ExampleFollowsTypeURL(o.Binding.ScopeURL), "example-cites": ExampleCitesTypeURL(o.Binding.ScopeURL)} {
				got, err := s.ReadType(ctx, strings.TrimPrefix(id, o.Binding.ScopeURL))
				if err != nil {
					t.Fatal(err)
				}
				var persisted []byte
				var fingerprint string
				if err := s.db.QueryRowContext(ctx, `SELECT descriptor,fingerprint FROM graph_preview_types WHERE name=?`, name).Scan(&persisted, &fingerprint); err != nil {
					t.Fatal(err)
				}
				if got.ID() != id || !bytes.Equal(got.CanonicalJSON(), persisted) || got.Fingerprint() != fingerprint {
					t.Fatalf("%s did not return persisted descriptor", name)
				}
			}
			if _, err := s.ReadType(ctx, "types/not-installed"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("missing Type: %v", err)
			}
			for _, path := range []string{"beads/plan", "types/" + strings.Repeat("x", 1024), "types/", "types/../preview-memory-v2", "types//preview-memory-v2", "types/preview-memory-v2?version=old", o.Binding.ScopeURL + "types/preview-memory-v2"} {
				if _, err := s.ReadType(ctx, path); !errors.Is(err, graph.ErrValidation) {
					t.Fatalf("invalid selector %q: %v", path, err)
				}
			}
			if err := s.db.QueryRowContext(ctx, `SELECT writer_token FROM graph_preview_scope WHERE singleton=1`).Scan(&writerAfter); err != nil {
				t.Fatal(err)
			}
			if writerBefore != writerAfter {
				t.Fatal("Type reads changed writer coordination state")
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = OpenExisting(ctx, o)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := s.ReadType(ctx, strings.TrimPrefix(MemoryTypeURL(o.Binding.ScopeURL), o.Binding.ScopeURL)); err != nil || got.ID() != MemoryTypeURL(o.Binding.ScopeURL) {
				t.Fatalf("reopened persisted Type: %v", err)
			}
		})
	}
}

func TestListInstalledTypesReflectsPersistedInstallation(t *testing.T) {
	ctx, options := issueExperimentOptions(t, "embedded")
	store, err := OpenExisting(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	}()
	var tokenBefore, tokenAfter string
	if err := store.db.QueryRowContext(ctx, `SELECT writer_token FROM graph_preview_scope WHERE singleton=1`).Scan(&tokenBefore); err != nil {
		t.Fatal(err)
	}
	installed, err := store.ListInstalledTypes(ctx)
	if err != nil || len(installed) != 6 {
		t.Fatalf("fresh installed Types: %d %v", len(installed), err)
	}
	rows, err := store.db.QueryContext(ctx, `SELECT descriptor FROM graph_preview_types`)
	if err != nil {
		t.Fatal(err)
	}
	persistedByID := map[string][]byte{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		descriptor, err := graph.ParseTypeDescriptor(raw)
		if err != nil {
			t.Fatal(err)
		}
		persistedByID[descriptor.ID()] = raw
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		t.Fatal(err)
	}
	for i, descriptor := range installed {
		if i > 0 && graph.CompareCodeUnits(installed[i-1].ID(), descriptor.ID()) >= 0 {
			t.Fatalf("Type inventory is not in stable ID order: %s", descriptor.ID())
		}
		if !bytes.Equal(descriptor.CanonicalJSON(), persistedByID[descriptor.ID()]) {
			t.Fatalf("%s did not return its persisted descriptor", descriptor.ID())
		}
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM graph_preview_types WHERE name IN ('example-follows','example-cites')`); err != nil {
		t.Fatal(err)
	}
	installed, err = store.ListInstalledTypes(ctx)
	if err != nil || len(installed) != 4 {
		t.Fatalf("older four-Type installation: %d %v", len(installed), err)
	}
	for _, descriptor := range installed {
		if strings.Contains(descriptor.ID(), "example-") {
			t.Fatalf("uninstalled example advertised: %s", descriptor.ID())
		}
	}
	if err := store.db.QueryRowContext(ctx, `SELECT writer_token FROM graph_preview_scope WHERE singleton=1`).Scan(&tokenAfter); err != nil || tokenAfter != tokenBefore {
		t.Fatalf("Type listing changed writer state: %q → %q, %v", tokenBefore, tokenAfter, err)
	}
}

func TestReadTypeRefusesInvalidInstallation(t *testing.T) {
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
			var raw []byte
			var fingerprint string
			if err := s.db.QueryRowContext(ctx, `SELECT descriptor,fingerprint FROM graph_preview_types WHERE name='memory'`).Scan(&raw, &fingerprint); err != nil {
				t.Fatal(err)
			}
			for _, corruption := range []string{"malformed", "fingerprint", "missing", "changed-contract", "binding"} {
				t.Run(corruption, func(t *testing.T) {
					switch corruption {
					case "malformed":
						_, err = s.db.ExecContext(ctx, `UPDATE graph_preview_types SET descriptor='{' WHERE name='memory'`)
					case "fingerprint":
						_, err = s.db.ExecContext(ctx, `UPDATE graph_preview_types SET fingerprint='bad' WHERE name='memory'`)
					case "missing":
						_, err = s.db.ExecContext(ctx, `DELETE FROM graph_preview_types WHERE name='memory'`)
					case "changed-contract":
						changed, buildErr := graph.NewTypeDescriptor(graph.TypeDescriptorSpec{ID: MemoryTypeURL(o.Binding.ScopeURL), Name: "Unauthorized replacement", Describes: graph.KindBead})
						if buildErr != nil {
							t.Fatal(buildErr)
						}
						_, err = s.db.ExecContext(ctx, `UPDATE graph_preview_types SET descriptor=?,fingerprint=? WHERE name='memory'`, changed.CanonicalJSON(), changed.Fingerprint())
					case "binding":
						_, err = s.db.ExecContext(ctx, `UPDATE graph_preview_scope SET workspace='another-workspace' WHERE singleton=1`)
					}
					if err != nil {
						t.Fatal(err)
					}
					// Invalid installation beats apparent absence even for an unknown Type.
					for _, path := range []string{strings.TrimPrefix(MemoryTypeURL(o.Binding.ScopeURL), o.Binding.ScopeURL), "types/not-installed"} {
						if _, err := s.ReadType(ctx, path); !errors.Is(err, ErrInvalidStore) {
							t.Fatalf("%s returned a Type/absence through corrupt installation: %v", path, err)
						}
					}
					if _, err := s.db.ExecContext(ctx, `REPLACE INTO graph_preview_types(name,descriptor,fingerprint) VALUES('memory',?,?)`, raw, fingerprint); err != nil {
						t.Fatal(err)
					}
					if _, err := s.db.ExecContext(ctx, `UPDATE graph_preview_scope SET workspace=? WHERE singleton=1`, o.Binding.WorkspaceID); err != nil {
						t.Fatal(err)
					}
				})
			}
		})
	}
}
