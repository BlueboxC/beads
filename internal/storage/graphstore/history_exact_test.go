//go:build cgo

package graphstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	graph "github.com/steveyegge/beads/graphops"
)

func TestReadVersionRetainedLifecycle(t *testing.T) {
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
			memory, err := s.Create(ctx, CreateRequest{Path: "beads/plan", Body: "Original — 雪", Actor: "author"})
			if err != nil {
				t.Fatal(err)
			}
			work, err := s.CreateIssue(ctx, "beads/work", plainIssue("Work"))
			if err != nil {
				t.Fatal(err)
			}
			prereq, err := s.CreateIssue(ctx, "beads/prereq", plainIssue("Prerequisite"))
			if err != nil {
				t.Fatal(err)
			}
			dep, err := s.AddDependency(ctx, DependencyRequest{Path: "links/block", SourcePath: "beads/work", TargetPath: "beads/prereq", Actor: "author"})
			if err != nil {
				t.Fatal(err)
			}
			closed, err := s.CloseIssue(ctx, "beads/prereq", "Done", "author")
			if err != nil {
				t.Fatal(err)
			}
			added, err := s.AddInformationalLink(ctx, LinkCreateRequest{Path: "links/context", SourcePath: "beads/plan", TargetPath: "beads/work", ExpectedSourceRevision: memory.Revision, Properties: map[string]any{"note": "before"}, Actor: "author"})
			if err != nil {
				t.Fatal(err)
			}
			updated, err := s.UpdateLink(ctx, LinkUpdateRequest{Path: "links/context", ExpectedRevision: added.Link.Revision, ExpectedSourceRevision: added.Source.(Record).Revision, Properties: map[string]any{"note": "after"}, Actor: "editor"})
			if err != nil {
				t.Fatal(err)
			}
			removed, err := s.Unlink(ctx, LinkDeleteRequest{Path: "links/context", ExpectedRevision: updated.Link.Revision, ExpectedSourceRevision: updated.Source.(Record).Revision, Actor: "remover"})
			if err != nil {
				t.Fatal(err)
			}
			cases := []struct {
				path, version string
				want          any
			}{
				{"beads/plan", memory.Revision, memory},
				{"beads/plan", added.Source.(Record).Revision, added.Source},
				{"beads/plan", updated.Source.(Record).Revision, updated.Source},
				{"beads/plan", removed.Source.(Record).Revision, removed.Source},
				{"links/context", added.Link.Revision, added.Link},
				{"links/context", updated.Link.Revision, updated.Link},
				{"beads/work", work.Revision, work},
				{"beads/work", dep.Source.Revision, dep.Source},
				{"beads/prereq", prereq.Revision, prereq},
				{"beads/prereq", closed.Issue.Revision, closed.Issue},
				{"links/block", dep.Link.Revision, dep.Link},
			}
			var tokenBefore, tokenAfter string
			if err := s.db.QueryRowContext(ctx, `SELECT writer_token FROM graph_preview_scope WHERE singleton=1`).Scan(&tokenBefore); err != nil {
				t.Fatal(err)
			}
			for _, pass := range []string{"before-reopen", "after-reopen"} {
				for _, tc := range cases {
					want := tc.want
					if issue, ok := want.(IssueRecord); ok {
						// Current hydration includes these two json:"-" fields.
						// Jim's durable_state intentionally retains neither the
						// computed content hash nor the current row-lock token.
						properties := *issue.Properties
						properties.ContentHash, properties.RowVersion = "", 0
						issue.Properties = &properties
						want = issue
					}
					got, err := s.ReadVersion(ctx, tc.path, tc.version)
					if err != nil || !reflect.DeepEqual(got, want) {
						t.Fatalf("%s %s/%s: %v\ngot=%+v\nwant=%+v", pass, tc.path, tc.version, err, got, want)
					}
				}
				if got, err := s.ReadVersion(ctx, "links/context", removed.Link.Revision); !errors.Is(err, ErrGone) || got != nil {
					t.Fatalf("private tombstone treated as Resource: %+v %v", got, err)
				}
				if got, err := s.ReadVersion(ctx, "beads/plan", added.Link.Revision); !errors.Is(err, ErrVersionUnknown) || got != nil {
					t.Fatalf("cross-subject token selected a record: %+v %v", got, err)
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				s, err = OpenExisting(ctx, o)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := s.db.QueryRowContext(ctx, `SELECT writer_token FROM graph_preview_scope WHERE singleton=1`).Scan(&tokenAfter); err != nil {
				t.Fatal(err)
			}
			if tokenBefore != tokenAfter {
				t.Fatal("exact retained reads changed writer state")
			}
			// Historic reads do not hydrate current payload or current owned sets.
			if _, err := s.db.ExecContext(ctx, `UPDATE graph_preview_payloads SET properties='{}' WHERE path='beads/plan'`); err != nil {
				t.Fatal(err)
			}
			got, err := s.ReadVersion(ctx, "beads/plan", added.Source.(Record).Revision)
			if err != nil || !reflect.DeepEqual(got, added.Source) {
				t.Fatalf("current payload contaminated exact history: %+v %v", got, err)
			}
		})
	}
}

func TestReadVersionRefusals(t *testing.T) {
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
			memory, err := s.Create(ctx, CreateRequest{Path: "beads/plan", Body: "Plan"})
			if err != nil {
				t.Fatal(err)
			}
			for _, token := range []string{"unknown", "a+b / 雪?%", strings.Repeat("x", PreviewVersionTokenLimit)} {
				if got, err := s.ReadVersion(ctx, "beads/plan", token); got != nil || !errors.Is(err, ErrVersionUnknown) {
					t.Fatalf("opaque token %q: %+v %v", token, got, err)
				}
			}
			for _, token := range []string{"", string([]byte{0xff}), strings.Repeat("x", PreviewVersionTokenLimit+1)} {
				if got, err := s.ReadVersion(ctx, "beads/plan", token); got != nil || !errors.Is(err, graph.ErrValidation) {
					t.Fatalf("invalid token accepted: %+v %v", got, err)
				}
			}
			if got, err := s.ReadVersion(ctx, "beads/absent", memory.Version); got != nil || !errors.Is(err, ErrNotFound) {
				t.Fatalf("absent subject: %+v %v", got, err)
			}
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			if got, err := s.ReadVersion(canceled, "beads/plan", memory.Version); got != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled read: %+v %v", got, err)
			}
			for name, statement := range map[string]string{
				"binding":          `UPDATE graph_preview_scope SET authority_id='00000000000000000000000000000000'`,
				"snapshot":         `UPDATE graph_preview_versions SET snapshot='{}'`,
				"positive-missing": `DELETE FROM graph_preview_versions`,
				"actor":            `UPDATE graph_preview_versions SET actor='different'`,
			} {
				t.Run(name, func(t *testing.T) {
					// Restore the exact retained row, including its ordinal and change_at,
					// so later cases probe the history the store actually wrote.
					var saved, savedActor []byte
					var savedOrdinal int64
					var savedChangeAt any
					if err := s.db.QueryRowContext(ctx, `SELECT snapshot, actor, ordinal, change_at FROM graph_preview_versions WHERE path='beads/plan' AND version=?`, memory.Version).Scan(&saved, &savedActor, &savedOrdinal, &savedChangeAt); err != nil {
						t.Fatal(err)
					}
					if _, err := s.db.ExecContext(ctx, statement); err != nil {
						t.Fatal(err)
					}
					got, readErr := s.ReadVersion(ctx, "beads/plan", memory.Version)
					if _, err := s.db.ExecContext(ctx, `UPDATE graph_preview_scope SET authority_id=?`, o.Binding.AuthorityID); err != nil {
						t.Fatal(err)
					}
					// Pre-existing latent bug fixed in passing: this restore used to write
					// actor='' instead of the saved actor, which was correct only by accident.
					if _, err := s.db.ExecContext(ctx, `REPLACE INTO graph_preview_versions(path,version,snapshot,actor,ordinal,change_at) VALUES(?,?,?,?,?,?)`, "beads/plan", memory.Version, saved, savedActor, savedOrdinal, savedChangeAt); err != nil {
						t.Fatal(err)
					}
					if !errors.Is(readErr, ErrInvalidStore) || got != nil {
						t.Fatalf("corruption leaked state: %+v %v", got, readErr)
					}
				})
			}
			// SQL-side sizing must reject before acquiring or decoding this blob.
			if _, err := s.db.ExecContext(ctx, `UPDATE graph_preview_versions SET snapshot=REPEAT('x',?)`, PreviewCurrentReadByteLimit+1); err != nil {
				t.Fatal(err)
			}
			if got, err := s.ReadVersion(ctx, "beads/plan", memory.Version); got != nil || !errors.Is(err, ErrLimitExceeded) {
				t.Fatalf("unbounded retained acquisition: %+v %v", got, err)
			}
		})
	}
}

// The exact Issue mapping fault is tested without altering Jim's recorder.
func TestReadVersionMissingIssueBody(t *testing.T) {
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
			issue, err := s.CreateIssue(ctx, "beads/work", plainIssue("Work"))
			if err != nil {
				t.Fatal(err)
			}
			err = s.withTx(ctx, false, func(tx *sql.Tx) error {
				if _, err := tx.ExecContext(ctx, `DELETE FROM issue_versions`); err != nil {
					return err
				}
				got, err := s.readIssueVersionInTx(ctx, tx, "beads/work", issue.Version, issue.Version, issue.Properties.ID)
				if !errors.Is(err, ErrInvalidStore) || !reflect.DeepEqual(got, IssueRecord{}) {
					return fmt.Errorf("missing mapped Issue body leaked state: %+v %v", got, err)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

// A retained owner may own only what its kind owns: informational Links for a
// Memory, Dependencies for an Issue. validateVersionOwned is handed the owner's
// Bead Type for a Memory but the one owned Link Type for an Issue, so the Memory
// Type must not pass as an owned Link Type. A Link of that Type under a Memory is
// an owned-membership fault, not one for the Dependency check to name later. The
// valid owned sets and the other cross-kind Types are controls: they hold the line
// on what a valid store accepts.
func TestReadVersionOwnedMembershipByOwnerKind(t *testing.T) {
	const membership = "invalid retained owned membership"
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, o := issueExperimentOptions(t, backend)
			s := openVersionsStore(t, ctx, o)
			scope := s.ScopeURL()
			memory, err := s.Create(ctx, CreateRequest{Path: "beads/plan", Body: "Plan", Actor: "author"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.CreateIssue(ctx, "beads/work", plainIssue("Work")); err != nil {
				t.Fatal(err)
			}
			if _, err := s.CreateIssue(ctx, "beads/prereq", plainIssue("Prerequisite")); err != nil {
				t.Fatal(err)
			}
			dep, err := s.AddDependency(ctx, DependencyRequest{Path: "links/block", SourcePath: "beads/work", TargetPath: "beads/prereq", Actor: "author"})
			if err != nil {
				t.Fatal(err)
			}
			added, err := s.AddInformationalLink(ctx, LinkCreateRequest{Path: "links/context", SourcePath: "beads/plan", TargetPath: "beads/work", ExpectedSourceRevision: memory.Revision, Actor: "author"})
			if err != nil {
				t.Fatal(err)
			}
			planVersion := added.Source.(Record).Version
			for _, tc := range []struct {
				name, path, version, linkType, refusal string
			}{
				{"memory-owns-informational", "beads/plan", planVersion, "", ""},
				{"issue-owns-dependency", "beads/work", dep.Source.Version, "", ""},
				{"memory-owns-memory-typed", "beads/plan", planVersion, MemoryTypeURL(scope), membership},
				{"memory-owns-dependency-typed", "beads/plan", planVersion, DependencyTypeURL(scope), membership},
				{"issue-owns-informational-typed", "beads/work", dep.Source.Version, RelatedTypeURL(scope), membership},
			} {
				t.Run(tc.name, func(t *testing.T) {
					if tc.linkType != "" {
						restore := retypeOwnedLink(t, ctx, s, tc.path, tc.version, tc.linkType)
						defer restore()
					}
					got, err := s.ReadVersion(ctx, tc.path, tc.version)
					if tc.refusal == "" {
						if err != nil || got == nil {
							t.Fatalf("valid owned set refused: %+v %v", got, err)
						}
						return
					}
					if got != nil || !errors.Is(err, ErrInvalidStore) || !strings.HasSuffix(err.Error(), ": "+tc.refusal) {
						t.Fatalf("want ErrInvalidStore ending %q, got %+v %v", tc.refusal, got, err)
					}
				})
			}
		})
	}
}

// retypeOwnedLink rewrites the Type of the first Link in one retained owned set
// and returns a func that puts the stored bytes back. Every enclosing value is
// re-encoded canonically, so the Link Type is the only fault left to find.
func retypeOwnedLink(t *testing.T, ctx context.Context, s *Store, path, version, typ string) func() {
	t.Helper()
	var backing string
	if err := s.db.QueryRowContext(ctx, `SELECT backing FROM graph_preview_catalog WHERE path=?`, path).Scan(&backing); err != nil {
		t.Fatal(err)
	}
	// An Issue keeps its owned set beside its mapping row, a Memory inside its snapshot.
	table, column := "graph_preview_versions", "snapshot"
	if backing == "issue" {
		table, column = "graph_preview_issue_versions", "owned"
	}
	var saved []byte
	if err := s.db.QueryRowContext(ctx, `SELECT `+column+` FROM `+table+` WHERE path=? AND version=?`, path, version).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	retype := func(owned []json.RawMessage) []json.RawMessage {
		if len(owned) == 0 {
			t.Fatalf("%s %s owns no Link to retype", path, version)
		}
		var link LinkRecord
		if err := json.Unmarshal(owned[0], &link); err != nil {
			t.Fatal(err)
		}
		link.Type = typ
		raw, err := canonicalJSON(link)
		if err != nil {
			t.Fatal(err)
		}
		return append([]json.RawMessage{raw}, owned[1:]...)
	}
	var value any
	if backing == "issue" {
		var owned []json.RawMessage
		if err := json.Unmarshal(saved, &owned); err != nil {
			t.Fatal(err)
		}
		value = retype(owned)
	} else {
		var memory Record
		if err := json.Unmarshal(saved, &memory); err != nil {
			t.Fatal(err)
		}
		memory.Owned = retype(memory.Owned)
		value = memory
	}
	rewritten, err := canonicalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	update := `UPDATE ` + table + ` SET ` + column + `=? WHERE path=? AND version=?`
	if _, err := s.db.ExecContext(ctx, update, rewritten, path, version); err != nil {
		t.Fatal(err)
	}
	return func() {
		if _, err := s.db.ExecContext(ctx, update, saved, path, version); err != nil {
			t.Error(err)
		}
	}
}
