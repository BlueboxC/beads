//go:build cgo

package graphstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func openVersionsStore(t *testing.T, ctx context.Context, o Options) *Store {
	t.Helper()
	s, err := OpenExisting(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}

// checkVersionList asserts the invariants every returned list must satisfy,
// independent of plane: newest first by ordinal, strictly descending, positive,
// a resolvable token on every row, and a populated change instant. marker names
// the one token allowed to refuse with ErrGone instead of resolving, which is a
// deleted Link's deletion marker; "" means every row must resolve.
//
// Removed is checked against marker on every row rather than only on the marker:
// the flag's whole contract is "listed, but this token is not citable", so a row
// that resolves and claims Removed is as wrong as a marker that does not claim it.
func checkVersionList(t *testing.T, ctx context.Context, s *Store, path string, rows []VersionRow, marker string) {
	t.Helper()
	if len(rows) == 0 {
		t.Fatalf("%s: empty version list", path)
	}
	for i, row := range rows {
		if row.Ordinal < 1 {
			t.Errorf("%s row %d: non-positive ordinal %d", path, i, row.Ordinal)
		}
		if i > 0 && row.Ordinal >= rows[i-1].Ordinal {
			t.Errorf("%s row %d: ordinal %d does not descend below %d", path, i, row.Ordinal, rows[i-1].Ordinal)
		}
		if row.ChangeAt.IsZero() || row.ChangeAt.Location() != time.UTC {
			t.Errorf("%s row %d: change instant %v is not a populated UTC time", path, i, row.ChangeAt)
		}
		if want := marker != "" && row.Version == marker; row.Removed != want {
			t.Errorf("%s row %d: Removed=%v, want %v (marker %q)", path, i, row.Removed, want, marker)
		}
		// Every token this reader hands out must address retained state; that is
		// the whole point of returning Version alongside the local ordinal.
		_, err := s.ReadVersion(ctx, path, row.Version)
		if row.Version == marker {
			if !errors.Is(err, ErrGone) {
				t.Errorf("%s row %d: deletion marker %q resolved as a Resource: %v", path, i, row.Version, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s row %d: listed token %q does not resolve: %v", path, i, row.Version, err)
		}
	}
}

// TestVersionsIssuePlaneRecordsWithoutTheConfigFlag verifies the premise this
// reader rests on rather than assuming it: a graph workspace holds native
// issue_versions rows for every accepted Issue mutation even though no
// versioned-history setting is enabled anywhere in it. Each graph Issue write
// path scopes recording on for its own transaction
// (issueops.ScopeVersionedHistoryTransaction(tx, true)), so the
// config default of false never reaches those writes. If this premise ever
// breaks, the Issue plane silently loses its ordering authority, so it is
// asserted directly against the tables, not inferred from the reader's output.
func TestVersionsIssuePlaneRecordsWithoutTheConfigFlag(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, o := issueExperimentOptions(t, backend)
			s := openVersionsStore(t, ctx, o)
			work, err := s.CreateIssue(ctx, "beads/work", plainIssue("Work"))
			if err != nil {
				t.Fatal(err)
			}
			prereq, err := s.CreateIssue(ctx, "beads/prereq", plainIssue("Prerequisite"))
			if err != nil {
				t.Fatal(err)
			}
			title := "Work, retitled — 記憶"
			updated, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "editor",
				ExpectedRevision: work.Revision, Title: &title})
			if err != nil {
				t.Fatal(err)
			}
			dep, err := s.AddDependency(ctx, DependencyRequest{Path: "links/block", SourcePath: "beads/work",
				TargetPath: "beads/prereq", ExpectedSourceRevision: updated.Issue.Revision, Actor: "author"})
			if err != nil {
				t.Fatal(err)
			}
			claimed, err := s.ClaimIssue(ctx, "beads/work", "claimant")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.CloseIssue(ctx, "beads/prereq", "Done", "closer"); err != nil {
				t.Fatal(err)
			}
			reopened, err := s.ReopenIssue(ctx, "beads/prereq", "Not done after all", "reopener")
			if err != nil {
				t.Fatal(err)
			}
			// No versioned-history setting exists in this workspace at all: the
			// preview installs exactly one config row, issue_prefix.
			var settings int
			if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM config WHERE `key` LIKE 'versioned-history%'").Scan(&settings); err != nil {
				t.Fatal(err)
			}
			if settings != 0 {
				t.Fatalf("workspace enables versioned history through config in %d rows; the premise under test is that it does not", settings)
			}
			for _, subject := range []struct {
				path, issueID, head string
				mutations           int
			}{
				{"beads/work", work.Properties.ID, claimed.Issue.Revision, 4},
				{"beads/prereq", prereq.Properties.ID, reopened.Issue.Revision, 3},
			} {
				var native int
				if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM issue_versions WHERE issue_id=?`, subject.issueID).Scan(&native); err != nil {
					t.Fatal(err)
				}
				if native < subject.mutations {
					t.Fatalf("%s: native plane recorded %d versions, want at least %d; recording is not being forced on", subject.path, native, subject.mutations)
				}
				kind, rows, err := s.Versions(ctx, subject.path)
				if err != nil {
					t.Fatal(err)
				}
				if kind != KindIssue {
					t.Fatalf("%s: kind=%q, want %q", subject.path, kind, KindIssue)
				}
				if len(rows) != subject.mutations {
					t.Fatalf("%s: %d versions, want %d: %+v", subject.path, len(rows), subject.mutations, rows)
				}
				// Evidence for why the Issue plane gets no dense-ordinal guard:
				// these ordinals are native revisions, and a native write that
				// advances one without minting a graph mapping leaves a
				// legitimate gap. Measured rather than asserted — the point is
				// that this reader does not own the number.
				t.Logf("%s: native issue_versions=%d mapped graph versions=%d oldest ordinal=%d newest ordinal=%d",
					subject.path, native, len(rows), rows[len(rows)-1].Ordinal, rows[0].Ordinal)
				checkVersionList(t, ctx, s, subject.path, rows, "")
				// The newest listed version is the current head, and the oldest
				// is the creating mutation's native ordinal 1.
				if rows[0].Version != subject.head {
					t.Errorf("%s: newest version %q, want current head %q", subject.path, rows[0].Version, subject.head)
				}
				if rows[len(rows)-1].Ordinal != 1 {
					t.Errorf("%s: oldest ordinal %d, want 1", subject.path, rows[len(rows)-1].Ordinal)
				}
				// Issues carry the native attribution_status; Memory and Link do not.
				for i, row := range rows {
					if row.Attribution == "" {
						t.Errorf("%s row %d: empty attribution status on the Issue plane", subject.path, i)
					}
				}
			}
			// Actor comes from the mutation that minted the version, not from
			// the subject's creator: the claim is the newest version of work.
			kind, rows, err := s.Versions(ctx, "beads/work")
			if err != nil || kind != KindIssue {
				t.Fatalf("work versions: %v %q", err, kind)
			}
			if rows[0].Actor != "claimant" {
				t.Errorf("newest work version actor=%q, want the claiming actor", rows[0].Actor)
			}
			// The owned Dependency is a Link with its own independent history.
			kind, links, err := s.Versions(ctx, "links/block")
			if err != nil {
				t.Fatal(err)
			}
			if kind != KindLink || len(links) != 1 || links[0].Ordinal != 1 || links[0].Version != dep.Link.Revision {
				t.Fatalf("Dependency versions: kind=%q rows=%+v", kind, links)
			}
			if links[0].Attribution != "" {
				t.Errorf("Link version claims attribution status %q; that column exists only on the native plane", links[0].Attribution)
			}
			checkVersionList(t, ctx, s, "links/block", links, "")
		})
	}
}

// TestVersionsMemoryAndLinkPlanes covers the graph_preview_versions plane, whose
// ordinal is allocated by the preview writer rather than by the Issue domain.
func TestVersionsMemoryAndLinkPlanes(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, o := issueExperimentOptions(t, backend)
			s := openVersionsStore(t, ctx, o)
			plan, err := s.Create(ctx, CreateRequest{Path: "beads/plan", Title: "Plan", Body: "Original — 雪", Actor: "author"})
			if err != nil {
				t.Fatal(err)
			}
			notes, err := s.Create(ctx, CreateRequest{Path: "beads/notes", Title: "Notes", Actor: "author"})
			if err != nil {
				t.Fatal(err)
			}
			revised, err := s.UpdateMemory(ctx, MemoryUpdateRequest{Path: "beads/plan",
				Properties: Properties{Title: "Plan", Body: "Revised"}, Actor: "editor", ExpectedRevision: plan.Revision})
			if err != nil {
				t.Fatal(err)
			}
			// A Memory owns its outgoing Links, so linking mints a Memory version
			// as well as the Link's own first version.
			added, err := s.AddInformationalLink(ctx, LinkCreateRequest{Path: "links/context",
				SourcePath: "beads/plan", TargetPath: "beads/notes", ExpectedSourceRevision: revised.Memory.Revision,
				Properties: map[string]any{"note": "before"}, Actor: "linker"})
			if err != nil {
				t.Fatal(err)
			}
			relinked, err := s.UpdateLink(ctx, LinkUpdateRequest{Path: "links/context",
				ExpectedRevision: added.Link.Revision, ExpectedSourceRevision: added.Source.(Record).Revision,
				Properties: map[string]any{"note": "after"}, Actor: "relinker"})
			if err != nil {
				t.Fatal(err)
			}
			kind, rows, err := s.Versions(ctx, "beads/plan")
			if err != nil {
				t.Fatal(err)
			}
			if kind != KindMemory {
				t.Fatalf("Memory kind=%q", kind)
			}
			// create, update, link (owned set changed), relink (owned set changed).
			if len(rows) != 4 {
				t.Fatalf("Memory versions: %d rows %+v", len(rows), rows)
			}
			checkVersionList(t, ctx, s, "beads/plan", rows, "")
			if rows[0].Version != relinked.Source.(Record).Revision || rows[len(rows)-1].Version != plan.Revision {
				t.Errorf("Memory list is not newest-first: %+v", rows)
			}
			if rows[0].Actor != "relinker" || rows[len(rows)-1].Actor != "author" {
				t.Errorf("Memory actors: newest=%q oldest=%q", rows[0].Actor, rows[len(rows)-1].Actor)
			}
			for i, row := range rows {
				if row.Attribution != "" {
					t.Errorf("Memory row %d claims attribution status %q", i, row.Attribution)
				}
			}
			kind, links, err := s.Versions(ctx, "links/context")
			if err != nil {
				t.Fatal(err)
			}
			if kind != KindLink || len(links) != 2 {
				t.Fatalf("Link versions: kind=%q rows=%+v", kind, links)
			}
			checkVersionList(t, ctx, s, "links/context", links, "")
			if links[0].Version != relinked.Link.Revision || links[1].Version != added.Link.Revision {
				t.Errorf("Link list is not newest-first: %+v", links)
			}
			if links[0].Ordinal != 2 || links[1].Ordinal != 1 {
				t.Errorf("Link ordinals %d,%d, want 2,1", links[0].Ordinal, links[1].Ordinal)
			}
			// A Memory that was never linked still has its own single version.
			kind, untouched, err := s.Versions(ctx, "beads/notes")
			if err != nil || kind != KindMemory || len(untouched) != 1 || untouched[0].Version != notes.Revision {
				t.Fatalf("unlinked Memory: kind=%q rows=%+v err=%v", kind, untouched, err)
			}
		})
	}
}

// TestVersionsOrderByOrdinalNotChangeAt pins the ordering authority on both
// planes. change_at is an observed wall clock: a clock step, a replayed write,
// or two mutations inside the same microsecond must not reorder a history, so
// the stored instants are deliberately set to disagree with the ordinals here.
func TestVersionsOrderByOrdinalNotChangeAt(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, o := issueExperimentOptions(t, backend)
			s := openVersionsStore(t, ctx, o)
			plan, err := s.Create(ctx, CreateRequest{Path: "beads/plan", Title: "Plan", Actor: "author"})
			if err != nil {
				t.Fatal(err)
			}
			revised, err := s.UpdateMemory(ctx, MemoryUpdateRequest{Path: "beads/plan",
				Properties: Properties{Title: "Plan", Body: "Revised"}, Actor: "editor", ExpectedRevision: plan.Revision})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.UpdateMemory(ctx, MemoryUpdateRequest{Path: "beads/plan",
				Properties: Properties{Title: "Plan", Body: "Revised again"}, Actor: "editor",
				ExpectedRevision: revised.Memory.Revision}); err != nil {
				t.Fatal(err)
			}
			issue, err := s.CreateIssue(ctx, "beads/work", plainIssue("Work"))
			if err != nil {
				t.Fatal(err)
			}
			title := "Work, retitled"
			if _, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "editor",
				ExpectedRevision: issue.Revision, Title: &title}); err != nil {
				t.Fatal(err)
			}
			// Invert both planes' instants against their ordinals: the oldest
			// version now carries the newest timestamp.
			base := time.Date(2031, 3, 1, 12, 0, 0, 0, time.UTC)
			for ordinal := 1; ordinal <= 3; ordinal++ {
				at := base.Add(time.Duration(-ordinal) * time.Hour)
				if _, err := s.db.ExecContext(ctx, `UPDATE graph_preview_versions SET change_at=? WHERE path='beads/plan' AND ordinal=?`, at, ordinal); err != nil {
					t.Fatal(err)
				}
				if _, err := s.db.ExecContext(ctx, `UPDATE issue_versions SET change_at=? WHERE issue_id=? AND revision=?`, at, issue.Properties.ID, ordinal); err != nil {
					t.Fatal(err)
				}
			}
			for _, path := range []string{"beads/plan", "beads/work"} {
				_, rows, err := s.Versions(ctx, path)
				if err != nil {
					t.Fatal(err)
				}
				if len(rows) < 2 {
					t.Fatalf("%s: %d versions", path, len(rows))
				}
				for i, row := range rows {
					if i > 0 && row.Ordinal >= rows[i-1].Ordinal {
						t.Fatalf("%s: ordinals stopped descending once change_at disagreed: %+v", path, rows)
					}
					// The inverted instants must come back ascending, which is
					// only possible if nothing sorted by them.
					if i > 0 && !row.ChangeAt.After(rows[i-1].ChangeAt) {
						t.Fatalf("%s: list follows change_at rather than ordinal: %+v", path, rows)
					}
				}
			}
		})
	}
}

// TestVersionsMissingSubjectIsNotAnEmptyList keeps the two reachable shapes
// distinct and pins that neither of them is an empty list: a path that was never
// allocated refuses with the package's existing ErrNotFound, and an allocated
// subject with nothing retained is corruption rather than a third, emptier
// answer. Creation IS version 1 on both planes — every allocation retains its
// first version in the same transaction as its catalog row — so zero retained
// rows for an allocated subject means that version was lost.
func TestVersionsMissingSubjectIsNotAnEmptyList(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, o := issueExperimentOptions(t, backend)
			s := openVersionsStore(t, ctx, o)
			for _, path := range []string{"beads/absent", "links/absent"} {
				kind, rows, err := s.Versions(ctx, path)
				if !errors.Is(err, ErrNotFound) || kind != "" || rows != nil {
					t.Fatalf("%s: kind=%q rows=%+v err=%v; want ErrNotFound", path, kind, rows, err)
				}
			}
			// A malformed path is a validation refusal, not an absence: the
			// caller asked an unanswerable question rather than a false one.
			if _, _, err := s.Versions(ctx, "not-a-resource"); err == nil || errors.Is(err, ErrNotFound) {
				t.Fatalf("malformed path: %v", err)
			}
			if _, err := s.Create(ctx, CreateRequest{Path: "beads/plan", Title: "Plan", Actor: "author"}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.CreateIssue(ctx, "beads/work", plainIssue("Work")); err != nil {
				t.Fatal(err)
			}
			// Drop the retained rows out of band. No writer can reach this state:
			// records.go createInTx and issues.go CreateIssue each insert the
			// first retained version in the same transaction as the catalog row,
			// so an allocated subject always has at least one version. That makes
			// this corruption to refuse, not an absence to report, and the
			// distinction is only observable if it is exercised.
			if _, err := s.db.ExecContext(ctx, `DELETE FROM graph_preview_versions WHERE path='beads/plan'`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.ExecContext(ctx, `DELETE FROM graph_preview_issue_versions WHERE path='beads/work'`); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"beads/plan", "beads/work"} {
				kind, rows, err := s.Versions(ctx, path)
				if !errors.Is(err, ErrInvalidStore) || kind != "" || rows != nil {
					t.Fatalf("%s with nothing retained: kind=%q rows=%+v err=%v; want ErrInvalidStore", path, kind, rows, err)
				}
			}
		})
	}
}

// TestVersionsRefusesCorruptRetainedState covers the ways this reader could
// otherwise shorten a history without saying so. Every one of them is
// ErrInvalidStore: a lost dual write is corruption whether all of it or part of
// it is gone, so the amount lost must not change the kind of answer. There is no
// "this plane cannot order" outcome left to test — both planes carry their
// ordering authority in the same transaction as the rows it orders, so a plane
// that cannot order has lost rows.
func TestVersionsRefusesCorruptRetainedState(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, o := issueExperimentOptions(t, backend)
			s := openVersionsStore(t, ctx, o)
			issue, err := s.CreateIssue(ctx, "beads/work", plainIssue("Work"))
			if err != nil {
				t.Fatal(err)
			}
			partial, err := s.CreateIssue(ctx, "beads/partial", plainIssue("Partial"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Create(ctx, CreateRequest{Path: "beads/plan", Title: "Plan", Actor: "author"}); err != nil {
				t.Fatal(err)
			}
			title := "Partial, retitled"
			if _, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/partial", Actor: "editor",
				ExpectedRevision: partial.Revision, Title: &title}); err != nil {
				t.Fatal(err)
			}
			// Mappings whose native rows are all gone leave the Issue plane with
			// nothing to order by, and mappings only some of which join leave it
			// able to order a shortened list. Same root cause — each mapping is
			// written in the same transaction as the native row it names — so
			// both answer the same way. Reporting either as an empty or short
			// list would let a lost history pass as a complete one.
			if _, err := s.db.ExecContext(ctx, `DELETE FROM issue_versions WHERE issue_id=?`, issue.Properties.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.ExecContext(ctx, `DELETE FROM issue_versions WHERE issue_id=? AND revision=1`, partial.Properties.ID); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"beads/work", "beads/partial"} {
				kind, rows, err := s.Versions(ctx, path)
				if !errors.Is(err, ErrInvalidStore) || kind != "" || rows != nil {
					t.Fatalf("%s lost its native rows: kind=%q rows=%+v err=%v; want ErrInvalidStore", path, kind, rows, err)
				}
			}
			// Retained rows with no allocation are corruption, never an absence.
			if _, err := s.db.ExecContext(ctx, `DELETE FROM graph_preview_catalog WHERE path='beads/plan'`); err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.Versions(ctx, "beads/plan"); !errors.Is(err, ErrInvalidStore) {
				t.Fatalf("orphan retained rows: %v", err)
			}
		})
	}
}

// TestVersionsRefusesAShortHistory covers the one failure mode this command must
// not have: a history truncated at the newest end, or with a hole punched in the
// middle, that comes back complete-looking — ordered, strictly descending, and
// simply shorter than the truth. Nothing in a list's own shape reveals it, so
// each case here is checked to confirm the guard it targets is the only one that
// can fire, rather than passing because some unrelated invariant tripped first.
func TestVersionsRefusesAShortHistory(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, o := issueExperimentOptions(t, backend)
			s := openVersionsStore(t, ctx, o)
			// beads/head loses its newest preview version; beads/gap loses one
			// from the middle. Separate subjects, because the out-of-band damage
			// cannot be undone.
			for _, subject := range []struct {
				path    string
				updates int
				drop    int64
				// dense says whether what remains is still 1..N. beads/head is,
				// so only the head check can refuse it; beads/gap is not, and its
				// head is still listed, so only the dense check can refuse it.
				dense bool
			}{{"beads/head", 1, 2, true}, {"beads/gap", 2, 2, false}} {
				record, err := s.Create(ctx, CreateRequest{Path: subject.path, Title: "Plan", Actor: "author"})
				if err != nil {
					t.Fatal(err)
				}
				revision := record.Revision
				for i := 0; i < subject.updates; i++ {
					// Distinct bodies: an identical rewrite is not guaranteed to
					// mint a version, and this case needs one per update.
					revised, err := s.UpdateMemory(ctx, MemoryUpdateRequest{Path: subject.path,
						Properties: Properties{Title: "Plan", Body: fmt.Sprintf("Revised %d", i)},
						Actor:      "editor", ExpectedRevision: revision})
					if err != nil {
						t.Fatal(err)
					}
					revision = revised.Memory.Revision
				}
				var retained, highest int64
				if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MAX(ordinal),0) FROM graph_preview_versions WHERE path=?`, subject.path).Scan(&retained, &highest); err != nil {
					t.Fatal(err)
				}
				if retained != int64(subject.updates)+1 {
					t.Fatalf("%s: %d retained versions before the damage, want %d", subject.path, retained, subject.updates+1)
				}
				if _, err := s.db.ExecContext(ctx, `DELETE FROM graph_preview_versions WHERE path=? AND ordinal=?`, subject.path, subject.drop); err != nil {
					t.Fatal(err)
				}
				// Confirm the damage is the shape this case means to test, so the
				// refusal below cannot be some other guard tripping first.
				if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MAX(ordinal),0) FROM graph_preview_versions WHERE path=?`, subject.path).Scan(&retained, &highest); err != nil {
					t.Fatal(err)
				}
				if (retained == highest) != subject.dense {
					t.Fatalf("%s: %d rows with highest ordinal %d, want dense=%v", subject.path, retained, highest, subject.dense)
				}
				kind, rows, err := s.Versions(ctx, subject.path)
				if !errors.Is(err, ErrInvalidStore) || kind != "" || rows != nil {
					t.Fatalf("%s short history: kind=%q rows=%+v err=%v; want ErrInvalidStore", subject.path, kind, rows, err)
				}
			}
			// The Issue plane needs the same head check, and the existing
			// mapped-count check cannot stand in for it: deleting one mapping row
			// lowers the mapping count and the join count together, so the list
			// stays self-consistent and only the catalog's head reveals the loss.
			work, err := s.CreateIssue(ctx, "beads/work", plainIssue("Work"))
			if err != nil {
				t.Fatal(err)
			}
			title := "Work, retitled"
			updated, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "editor",
				ExpectedRevision: work.Revision, Title: &title})
			if err != nil {
				t.Fatal(err)
			}
			head := updated.Issue.Revision
			if _, err := s.db.ExecContext(ctx, `DELETE FROM graph_preview_issue_versions WHERE path='beads/work' AND version=?`, head); err != nil {
				t.Fatal(err)
			}
			var mapped, joined int
			if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM graph_preview_issue_versions WHERE path='beads/work'`).Scan(&mapped); err != nil {
				t.Fatal(err)
			}
			if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM graph_preview_issue_versions m
 JOIN issue_versions v ON v.issue_id=m.issue_id AND v.revision=m.issue_revision WHERE m.path='beads/work'`).Scan(&joined); err != nil {
				t.Fatal(err)
			}
			if mapped != joined || mapped == 0 {
				t.Fatalf("Issue plane: %d mappings and %d joining rows; this case needs them equal and non-zero so only the head check can refuse", mapped, joined)
			}
			kind, rows, err := s.Versions(ctx, "beads/work")
			if !errors.Is(err, ErrInvalidStore) || kind != "" || rows != nil {
				t.Fatalf("Issue plane lost its head mapping: kind=%q rows=%+v err=%v; want ErrInvalidStore", kind, rows, err)
			}
		})
	}
}

// TestVersionsRefusesAnOversizedHistory pins that both acquisition bounds refuse
// rather than truncate. A truncating LIMIT would look helpful and would make an
// incomplete history indistinguishable from a complete one, which is the failure
// this whole reader is built to avoid — so the bounds are exercised rather than
// trusted. They are injected instead of writing a thousand real versions, and
// injected as a parameter rather than through a package-level var, which a
// parallel test in this package would race.
func TestVersionsRefusesAnOversizedHistory(t *testing.T) {
	if got := shippedVersionBounds(); got.rows != PreviewVersionListLimit || got.bytes != PreviewCurrentReadByteLimit {
		t.Fatalf("shipped bounds %+v no longer match the exported limits (%d rows, %d bytes)", got, PreviewVersionListLimit, PreviewCurrentReadByteLimit)
	}
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, o := issueExperimentOptions(t, backend)
			s := openVersionsStore(t, ctx, o)
			plan, err := s.Create(ctx, CreateRequest{Path: "beads/plan", Title: "Plan", Actor: "author"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.UpdateMemory(ctx, MemoryUpdateRequest{Path: "beads/plan",
				Properties: Properties{Title: "Plan", Body: "Revised"}, Actor: "editor", ExpectedRevision: plan.Revision}); err != nil {
				t.Fatal(err)
			}
			work, err := s.CreateIssue(ctx, "beads/work", plainIssue("Work"))
			if err != nil {
				t.Fatal(err)
			}
			title := "Work, retitled"
			if _, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "editor",
				ExpectedRevision: work.Revision, Title: &title}); err != nil {
				t.Fatal(err)
			}
			// Both subjects have two versions, so both are within the shipped
			// bounds: the refusals below are the bound's doing, not the subject's.
			for _, path := range []string{"beads/plan", "beads/work"} {
				if _, rows, err := s.Versions(ctx, path); err != nil || len(rows) != 2 {
					t.Fatalf("%s under the shipped bounds: %d rows, err=%v", path, len(rows), err)
				}
			}
			for _, refusal := range []struct {
				name   string
				path   string
				bounds versionBounds
			}{
				// The row bound is charged on both planes; the actor-byte budget
				// is the preview plane's alone, since only it stores actor as a
				// blob that has to be charged before acquisition.
				{"row bound, preview plane", "beads/plan", versionBounds{rows: 1, bytes: PreviewCurrentReadByteLimit}},
				{"row bound, Issue plane", "beads/work", versionBounds{rows: 1, bytes: PreviewCurrentReadByteLimit}},
				{"actor-byte budget", "beads/plan", versionBounds{rows: PreviewVersionListLimit, bytes: 64}},
			} {
				kind, rows, err := s.versionsWithin(ctx, refusal.path, refusal.bounds)
				if !errors.Is(err, ErrLimitExceeded) || rows != nil || kind != "" {
					t.Fatalf("%s (%s): kind=%q rows=%+v err=%v; want ErrLimitExceeded and no rows",
						refusal.name, refusal.path, kind, rows, err)
				}
			}
		})
	}
}

// TestVersionsOfDeletedSubjects pins what survives deletion: a deleted subject
// keeps its history rather than becoming an absence.
func TestVersionsOfDeletedSubjects(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, o := issueExperimentOptions(t, backend)
			s := openVersionsStore(t, ctx, o)
			plan, err := s.Create(ctx, CreateRequest{Path: "beads/plan", Title: "Plan", Actor: "author"})
			if err != nil {
				t.Fatal(err)
			}
			notes, err := s.Create(ctx, CreateRequest{Path: "beads/notes", Title: "Notes", Actor: "author"})
			if err != nil {
				t.Fatal(err)
			}
			added, err := s.AddInformationalLink(ctx, LinkCreateRequest{Path: "links/context",
				SourcePath: "beads/plan", TargetPath: "beads/notes", ExpectedSourceRevision: plan.Revision,
				Properties: map[string]any{"note": "before"}, Actor: "linker"})
			if err != nil {
				t.Fatal(err)
			}
			removed, err := s.Unlink(ctx, LinkDeleteRequest{Path: "links/context",
				ExpectedRevision: added.Link.Revision, ExpectedSourceRevision: added.Source.(Record).Revision,
				Actor: "remover"})
			if err != nil {
				t.Fatal(err)
			}
			kind, links, err := s.Versions(ctx, "links/context")
			if err != nil {
				t.Fatal(err)
			}
			if kind != KindLink {
				t.Fatalf("deleted Link kind=%q", kind)
			}
			// The reader reports what the table holds, so the deletion marker is
			// listed rather than filtered out: a removal is part of the history.
			// It is the one listed token ReadVersion refuses, with ErrGone, and
			// Removed is how the caller is told that — it renders the row and
			// leaves it out of any "cite this token" guidance.
			checkVersionList(t, ctx, s, "links/context", links, removed.Link.Revision)
			if len(links) != 2 || links[0].Version != removed.Link.Revision || links[1].Version != added.Link.Revision {
				t.Fatalf("deleted Link history: %+v", links)
			}
			if !links[0].Removed {
				t.Errorf("deletion marker %q is not flagged Removed: %+v", removed.Link.Revision, links[0])
			}
			if links[1].Removed {
				t.Errorf("a resolvable Link version is flagged Removed: %+v", links[1])
			}
			// A deleted Memory mints no successor version, so its final live head
			// stays its newest retained version and stays readable.
			deleted, err := s.DeleteMemory(ctx, MemoryDeleteRequest{Path: "beads/notes",
				ExpectedRevision: notes.Revision, Actor: "remover"})
			if err != nil || !deleted.Deleted {
				t.Fatalf("delete Memory: %+v %v", deleted, err)
			}
			kind, rows, err := s.Versions(ctx, "beads/notes")
			if err != nil {
				t.Fatal(err)
			}
			if kind != KindMemory || len(rows) != 1 || rows[0].Version != notes.Revision {
				t.Fatalf("deleted Memory history: kind=%q rows=%+v", kind, rows)
			}
			// Deletion alone does not make a row uncitable: this head still
			// resolves, so it is not flagged.
			if rows[0].Removed {
				t.Errorf("a deleted Memory's resolvable head is flagged Removed: %+v", rows[0])
			}
			checkVersionList(t, ctx, s, "beads/notes", rows, "")
		})
	}
}

// TestVersionsOfExampleInformationalLinks pins that a listing accepts every
// informational Link Type the store has installed, not only the original
// Related one, and refuses an informational Type the store has not installed.
//
// This fails on the code before it: versionsInTx compared the informational
// backing's type_url against RelatedTypeURL alone, so every Link of the
// example-follows and example-cites Types, live or deleted, was refused as an
// unsupported retained allocation (ErrInvalidStore) while ReadVersion resolved
// the very tokens the list should have returned. The legacy half pins the other
// direction: accepting the example Types must not let a four-Type installation
// list an allocation of a Type it never installed, which ReadVersion refuses.
func TestVersionsOfExampleInformationalLinks(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, o := issueExperimentOptions(t, backend)
			s := openVersionsStore(t, ctx, o)
			policy, err := s.Create(ctx, CreateRequest{Path: "beads/policy", Title: "Policy", Body: "Code flow policy", Actor: "author"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.CreateIssue(ctx, "beads/work", plainIssue("Work")); err != nil {
				t.Fatal(err)
			}
			// The Memory owns each Link it is the source of, so every write to
			// such a Link also mints a Memory version that the next write guards.
			source := policy.Revision
			// Every example allocation and one of its tokens, for the legacy half.
			type allocation struct{ path, token string }
			var allocated []allocation
			for _, typ := range []string{ExampleCitesTypeURL(s.ScopeURL()), ExampleFollowsTypeURL(s.ScopeURL())} {
				name := strings.TrimPrefix(typ, s.ScopeURL()+"types/")
				path := "links/" + name
				added, err := s.AddInformationalLink(ctx, LinkCreateRequest{Path: path, TypeURL: typ,
					SourcePath: "beads/policy", TargetPath: "beads/work", ExpectedSourceRevision: source,
					Properties: map[string]any{"note": "before"}, Actor: "linker"})
				if err != nil || added.Link.Type != typ {
					t.Fatalf("%s: add: %+v %v", name, added, err)
				}
				edited, err := s.UpdateLink(ctx, LinkUpdateRequest{Path: path,
					ExpectedRevision: added.Link.Revision, ExpectedSourceRevision: added.Source.(Record).Revision,
					Properties: map[string]any{"note": "after"}, Actor: "editor"})
				if err != nil || !edited.Changed || edited.Link.Type != typ {
					t.Fatalf("%s: update: %+v %v", name, edited, err)
				}
				kind, rows, err := s.Versions(ctx, path)
				if err != nil {
					t.Fatalf("%s: live Link versions: %v", name, err)
				}
				if kind != KindLink || len(rows) != 2 || rows[0].Version != edited.Link.Revision || rows[1].Version != added.Link.Revision {
					t.Fatalf("%s: live Link history: kind=%q rows=%+v", name, kind, rows)
				}
				checkVersionList(t, ctx, s, path, rows, "")
				removed, err := s.Unlink(ctx, LinkDeleteRequest{Path: path,
					ExpectedRevision: edited.Link.Revision, ExpectedSourceRevision: edited.Source.(Record).Revision,
					Actor: "remover"})
				if err != nil || removed.Link.Type != typ {
					t.Fatalf("%s: unlink: %+v %v", name, removed, err)
				}
				source = removed.Source.(Record).Revision
				kind, rows, err = s.Versions(ctx, path)
				if err != nil {
					t.Fatalf("%s: deleted Link versions: %v", name, err)
				}
				// create, update, unlink: the deletion marker is a retained version
				// of its own, listed newest and the only row flagged Removed.
				if kind != KindLink || len(rows) != 3 || rows[0].Version != removed.Link.Revision ||
					rows[1].Version != edited.Link.Revision || rows[2].Version != added.Link.Revision {
					t.Fatalf("%s: deleted Link history: kind=%q rows=%+v", name, kind, rows)
				}
				if rows[0].Ordinal != 3 || rows[1].Ordinal != 2 || rows[2].Ordinal != 1 {
					t.Errorf("%s: deleted Link ordinals %d,%d,%d, want 3,2,1", name, rows[0].Ordinal, rows[1].Ordinal, rows[2].Ordinal)
				}
				if !rows[0].Removed || rows[1].Removed || rows[2].Removed {
					t.Errorf("%s: only the deletion marker may be flagged Removed: %+v", name, rows)
				}
				checkVersionList(t, ctx, s, path, rows, removed.Link.Revision)
				// An Issue source does not own its Links, so this one stays live
				// without advancing the Memory's history.
				kept, err := s.AddInformationalLink(ctx, LinkCreateRequest{Path: path + "-live", TypeURL: typ,
					SourcePath: "beads/work", TargetPath: "beads/policy", Actor: "linker"})
				if err != nil {
					t.Fatalf("%s: add live: %v", name, err)
				}
				// It lists before the store is altered below, so its refusal there
				// is the installation check firing, not a fixture that never worked.
				if kind, rows, err := s.Versions(ctx, path+"-live"); err != nil || kind != KindLink || len(rows) != 1 || rows[0].Version != kept.Link.Revision {
					t.Fatalf("%s: kept Link history: kind=%q rows=%+v err=%v", name, kind, rows, err)
				}
				allocated = append(allocated, allocation{path, added.Link.Version}, allocation{path + "-live", kept.Link.Version})
			}
			related, err := s.AddInformationalLink(ctx, LinkCreateRequest{Path: "links/related",
				SourcePath: "beads/policy", TargetPath: "beads/work", ExpectedSourceRevision: source, Actor: "linker"})
			if err != nil || related.Link.Type != RelatedTypeURL(s.ScopeURL()) {
				t.Fatalf("Related Link: %+v %v", related, err)
			}
			// Removing both example rows leaves exactly the original four-Type
			// installation, as in TestExampleTypesLegacyInstallation. Such a store
			// can hold an example allocation only if it is corrupt.
			if _, err := s.db.ExecContext(ctx, `DELETE FROM graph_preview_types WHERE name IN ('example-follows','example-cites')`); err != nil {
				t.Fatal(err)
			}
			for _, subject := range allocated {
				kind, rows, err := s.Versions(ctx, subject.path)
				if !errors.Is(err, ErrInvalidStore) || kind != "" || rows != nil {
					t.Fatalf("%s on a four-Type installation: kind=%q rows=%+v err=%v; want ErrInvalidStore", subject.path, kind, rows, err)
				}
				// Parity is the contract: the single read refuses this allocation
				// too, and the list must not be the lenient one.
				if _, err := s.ReadVersion(ctx, subject.path, subject.token); !errors.Is(err, ErrInvalidStore) {
					t.Fatalf("%s single read on a four-Type installation: %v; want ErrInvalidStore", subject.path, err)
				}
			}
			// The refusal belongs to the uninstalled Types, not to the informational
			// backing: the installed Related Type still lists on the same store.
			kind, rows, err := s.Versions(ctx, "links/related")
			if err != nil || kind != KindLink || len(rows) != 1 || rows[0].Version != related.Link.Revision {
				t.Fatalf("Related Link on a four-Type installation: kind=%q rows=%+v err=%v", kind, rows, err)
			}
		})
	}
}

// TestVersionsRefusesAMismatchedTypeURL pins that a LIST validates the catalog's
// type_url the same way a single-version READ does.
//
// This fails on the code before it: versionsInTx did not select type_url at all,
// so it established only that the backing and kind were a recognised pair. A
// subject with backing "generic" and some other Type's URL -- or a type_url long
// enough to be a storage problem in its own right -- listed happily while
// ReadVersion on the very tokens it returned refused as corrupt. Two readers over
// one catalog disagreeing about what a valid allocation is was the defect; the
// assertion here is that they now agree, which is why each case checks BOTH.
func TestVersionsRefusesAMismatchedTypeURL(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, o := issueExperimentOptions(t, backend)
			s := openVersionsStore(t, ctx, o)
			if _, err := s.Create(ctx, CreateRequest{Path: "beads/plan", Title: "Plan", Actor: "author"}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.CreateIssue(ctx, "beads/work", plainIssue("Work")); err != nil {
				t.Fatal(err)
			}
			// Both subjects list before the corruption, so a later refusal is the
			// type_url check firing rather than the fixture never having worked.
			for _, path := range []string{"beads/plan", "beads/work"} {
				if _, rows, err := s.Versions(ctx, path); err != nil || len(rows) == 0 {
					t.Fatalf("%s should list before corruption: rows=%d err=%v", path, len(rows), err)
				}
			}

			// Oversized is its own case, not a louder version of "wrong". It
			// selects as NULL through the OCTET_LENGTH bound rather than
			// comparing unequal, so it exercises the !typ.Valid branch.
			oversized := s.ScopeURL() + strings.Repeat("x", 300)
			for _, tc := range []struct {
				name, path, typ string
			}{
				{"memory-wrong-type", "beads/plan", s.ScopeURL() + "types/not-a-memory"},
				{"memory-oversized-type", "beads/plan", oversized},
				{"issue-wrong-type", "beads/work", s.ScopeURL() + "types/not-an-issue"},
				{"issue-oversized-type", "beads/work", oversized},
			} {
				t.Run(tc.name, func(t *testing.T) {
					var original string
					if err := s.db.QueryRowContext(ctx, `SELECT type_url FROM graph_preview_catalog WHERE path=?`, tc.path).Scan(&original); err != nil {
						t.Fatal(err)
					}
					if _, err := s.db.ExecContext(ctx, `UPDATE graph_preview_catalog SET type_url=? WHERE path=?`, tc.typ, tc.path); err != nil {
						t.Fatal(err)
					}
					// Restore before the next case so each one starts from a
					// healthy subject and a failure cannot cascade.
					t.Cleanup(func() {
						if _, err := s.db.ExecContext(ctx, `UPDATE graph_preview_catalog SET type_url=? WHERE path=?`, original, tc.path); err != nil {
							t.Fatal(err)
						}
					})
					kind, rows, err := s.Versions(ctx, tc.path)
					if !errors.Is(err, ErrInvalidStore) || kind != "" || rows != nil {
						t.Fatalf("list: kind=%q rows=%+v err=%v; want ErrInvalidStore", kind, rows, err)
					}
					// Parity is the actual contract: the single read already
					// refused this, and the list must not be the lenient one.
					if _, err := s.ReadVersion(ctx, tc.path, "any-token"); !errors.Is(err, ErrInvalidStore) {
						t.Fatalf("single read: %v; want ErrInvalidStore so both readers agree", err)
					}
				})
			}
		})
	}
}

// TestVersionsIsAReadOnlyOperation pins that listing history changes no writer
// coordination state, the same guarantee the exact retained reader gives.
//
// It loops BOTH backends rather than pinning embedded, because writer_token is
// the cell the store-wide write fence contends on (see insertPreviewVersionInTx):
// a reader that disturbed it would be stealing the fence from a concurrent
// writer, which is a server-plane concern first and an embedded one second.
// Verifying this only on embedded would leave the claim untested exactly where
// it matters most.
func TestVersionsIsAReadOnlyOperation(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, o := issueExperimentOptions(t, backend)
			s := openVersionsStore(t, ctx, o)
			if _, err := s.Create(ctx, CreateRequest{Path: "beads/plan", Title: "Plan", Actor: "author"}); err != nil {
				t.Fatal(err)
			}
			var before, after string
			if err := s.db.QueryRowContext(ctx, `SELECT writer_token FROM graph_preview_scope WHERE singleton=1`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.Versions(ctx, "beads/plan"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.Versions(ctx, "beads/absent"); !errors.Is(err, ErrNotFound) {
				t.Fatal(err)
			}
			if err := s.db.QueryRowContext(ctx, `SELECT writer_token FROM graph_preview_scope WHERE singleton=1`).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if before != after {
				t.Fatal("listing versions changed writer coordination state")
			}
		})
	}
}
