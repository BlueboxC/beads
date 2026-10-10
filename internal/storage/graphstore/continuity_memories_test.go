//go:build cgo

package graphstore

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	publicops "github.com/steveyegge/beads/issueops"
	"github.com/steveyegge/beads/memoryops"
)

// A real transaction boundary must retain human history while guarded derived
// replacements, pointers and writer tokens either all land or all roll back.
func TestContinuityMemoriesGraphPersistenceAndAtomicity(t *testing.T) {
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
			m := s.Memories("BlueboxC")
			const key = "@knowledge/record/solución%_"
			const content = "  verified\nUnicode: 記憶  "
			if _, err := m.Remember(ctx, memoryops.RememberRequest{Key: key, Content: content}); err != nil {
				t.Fatal(err)
			}
			plane, snap, err := s.ReadContinuity(ctx)
			if err != nil || plane.Memories[key] != content || len(snap.Records) != 1 {
				t.Fatalf("read plane=%v records=%d err=%v", plane, len(snap.Records), err)
			}
			first := snap.Records[0].(Record)
			path := strings.TrimPrefix(first.ID, s.ScopeURL())
			equalFault := errors.New("equal-value write fault")
			s.afterWrite = func(stage string) error {
				if stage == "continuity-pointer" {
					return equalFault
				}
				return nil
			}
			if _, err := m.Remember(ctx, memoryops.RememberRequest{Key: key, Content: content}); !errors.Is(err, equalFault) {
				t.Fatalf("equal-value Remember skipped its write: %v", err)
			}
			s.afterWrite = nil
			_, unchanged, err := s.ReadContinuity(ctx)
			if err != nil || unchanged.Records[0].(Record).Version != first.Version {
				t.Fatalf("equal-value write changed history: %v", err)
			}
			if _, err := s.Create(ctx, CreateRequest{Path: "beads/forged", Title: "Forged", Metadata: json.RawMessage(`{"forkContinuityKey":null}`)}); !errors.Is(err, storage.ErrValidation) {
				t.Fatalf("reserved metadata admitted: %v", err)
			}
			if _, err := s.PatchMemory(ctx, MemoryPatchRequest{Path: path, ExpectedRevision: first.Revision, Metadata: publicops.MetadataPatch{Unset: []string{ContinuityKeyMetadata}}}); !errors.Is(err, storage.ErrValidation) {
				t.Fatalf("managed key was detached: %v", err)
			}

			if got, err := m.List(ctx, memoryops.ListRequest{KeyPrefix: "@knowledge/record/solución%_", Search: "UNICODE"}); err != nil || len(got.Memories) != 1 {
				t.Fatalf("literal namespace/search %v %v", got, err)
			}
			if _, err := m.Remember(ctx, memoryops.RememberRequest{Key: key, Content: "new body"}); err != nil {
				t.Fatal(err)
			}
			// Existing retained API proves an update did not overwrite the prior version.
			old, err := s.ReadVersion(ctx, path, first.Version)
			if err != nil {
				t.Fatal(err)
			}
			if record, ok := old.(Record); !ok || record.Properties.Body != content {
				t.Fatalf("lost retained Memory: %+v", old)
			}
			const derived = "@knowledge/code/manifest"
			if _, err := m.Apply(ctx, memoryops.BatchRequest{Expected: map[string]string{derived: ""}, Remember: map[string]string{derived: "derived-v1"}}); err != nil {
				t.Fatal(err)
			}
			fault := errors.New("injected pointer publication fault")
			s.afterWrite = func(stage string) error {
				if stage == "continuity-pointer" {
					return fault
				}
				return nil
			}
			_, err = m.Apply(ctx, memoryops.BatchRequest{Expected: map[string]string{derived: "derived-v1"}, Remember: map[string]string{derived: "derived-v2", "@knowledge/proposal/new": "pending"}})
			s.afterWrite = nil
			if !errors.Is(err, fault) {
				t.Fatalf("want atomic fault: %v", err)
			}
			got, err := m.Recall(ctx, memoryops.RecallRequest{Key: derived})
			if err != nil || got.Value != "derived-v1" {
				t.Fatalf("partial derived commit %+v %v", got, err)
			}
			absent, err := m.Recall(ctx, memoryops.RecallRequest{Key: "@knowledge/proposal/new"})
			if err != nil || absent.Found {
				t.Fatalf("partial pointer commit %+v %v", absent, err)
			}
			if _, err := m.Apply(ctx, memoryops.BatchRequest{Expected: map[string]string{derived: "stale"}, Remember: map[string]string{key: "must not land"}}); !errors.Is(err, ErrConflict) {
				t.Fatalf("stale batch allowed: %v", err)
			}
			_, snap, err = s.ReadContinuity(ctx)
			if err != nil || len(snap.Records) != 1 {
				t.Fatalf("derived data entered catalog: %d %v", len(snap.Records), err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = OpenExisting(ctx, o)
			if err != nil {
				t.Fatal(err)
			}
			m = s.Memories("BlueboxC")
			got, err = m.Recall(ctx, memoryops.RecallRequest{Key: key})
			if err != nil || got.Value != "new body" {
				t.Fatalf("reopen lost human knowledge %+v %v", got, err)
			}
			target, err := s.Create(ctx, CreateRequest{Path: "beads/target", Title: "Target"})
			if err != nil {
				t.Fatal(err)
			}
			link, err := s.AddInformationalLink(ctx, LinkCreateRequest{SourcePath: path, TargetPath: "beads/target", UnconditionalSource: true})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := m.Forget(ctx, memoryops.ForgetRequest{Key: key}); !errors.Is(err, ErrIncidentLinkConstraint) {
				t.Fatalf("forget bypassed incident Links: %v", err)
			}
			if _, err := s.Unlink(ctx, LinkDeleteRequest{Path: strings.TrimPrefix(link.Link.ID, s.ScopeURL()), ExpectedRevision: link.Link.Revision, UnconditionalSource: true}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DeleteMemory(ctx, MemoryDeleteRequest{Path: "beads/target", ExpectedRevision: target.Revision}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DeleteMemory(ctx, MemoryDeleteRequest{Path: path, Unconditional: true}); err != nil {
				t.Fatal(err)
			}
			got, err = m.Recall(ctx, memoryops.RecallRequest{Key: key})
			if err != nil || got.Found {
				t.Fatalf("canonical deletion left pointer %+v %v", got, err)
			}
			if _, err := m.Remember(ctx, memoryops.RememberRequest{Key: key, Content: "recreated"}); err != nil {
				t.Fatal(err)
			}
			_, snap, err = s.ReadContinuity(ctx)
			if err != nil || snap.Records[0].(Record).ID == first.ID {
				t.Fatalf("reused tombstoned identity %v", err)
			}
			if _, err := s.ReadVersion(ctx, path, first.Version); err != nil {
				t.Fatalf("forget erased history: %v", err)
			}
			// Corruption is an error, never an apparently empty plane.
			if err := s.withTx(ctx, true, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, "UPDATE config SET value='missing-pointer' WHERE `key`=?", "kv.memory."+key)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := m.List(ctx, memoryops.ListRequest{}); !errors.Is(err, ErrInvalidStore) {
				t.Fatalf("corruption appeared absent: %v", err)
			}
		})
	}
}
