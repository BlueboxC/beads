//go:build cgo

package graphstore

import (
	"errors"
	"net"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func TestIssueDueLostCommitResponse(t *testing.T) {
	ctx, options, direct, original, target, dependency := reopenFixture(t, "server")
	claimed, err := direct.ClaimIssue(ctx, "beads/work", "holder")
	if err != nil || !claimed.Changed {
		t.Fatalf("claim fixture: changed=%t err=%v", claimed.Changed, err)
	}
	before := claimed.Issue
	var beforeEvents int
	if err := direct.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE issue_id=?", before.Properties.ID).Scan(&beforeEvents); err != nil {
		t.Fatal(err)
	}
	proxyPort, observed := startCommitLossProxy(t, net.JoinHostPort(options.ServerHost, strconv.Itoa(options.ServerPort)))
	proxied := options
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
	attempts := 0
	s.afterWrite = func(stage string) error {
		if stage == "coordination" {
			attempts++
		}
		return nil
	}
	input := time.Date(2036, 5, 6, 7, 8, 9, 750000000, time.FixedZone("east", 5*3600+1800))
	want := time.Date(2036, 5, 6, 1, 38, 10, 0, time.UTC)
	// Unconditional authoring makes retry observable even without a stale
	// revision guard. Unknown outcome must also suppress predecessor disclosure.
	got, err := s.UpdateIssue(ctx, UpdateIssueRequest{Path: "beads/work", Actor: "lost-due", Unconditional: true, DueAt: issueDueField(&input), Title: issueEditString("Committed due and title")})
	if !errors.Is(err, ErrOutcomeUnknown) || errors.Is(err, ErrConflict) || !reflect.DeepEqual(got, IssueMutationResult{}) || attempts != 1 {
		t.Fatalf("lost COMMIT returned a result or replayed: attempts=%d err=%v", attempts, err)
	}
	select {
	case packet := <-observed:
		if len(packet) == 0 || packet[0] != 0 {
			t.Fatalf("no real COMMIT success witness: %x", packet)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// Inspect through the independent direct connection; never replay the write.
	current, err := direct.ShowIssue(ctx, "beads/work")
	if err != nil {
		t.Fatal(err)
	}
	assertIssueDueTransition(t, before, current, &want, "Committed due and title", "lost-due")
	assertClaimLease(t, ctx, direct, original.Properties.ID, "holder")
	assertDependencyOwned(t, current, dependency)
	assertIssueEditCounts(t, ctx, direct, original.Properties.ID, 4)
	for _, saved := range []IssueRecord{original, before, current} {
		assertIssueEditVersion(t, ctx, direct, "beads/work", saved)
	}
	var afterEvents, writerEvents int
	if err := direct.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE issue_id=?", original.Properties.ID).Scan(&afterEvents); err != nil {
		t.Fatal(err)
	}
	if err := direct.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE issue_id=? AND actor='lost-due' AND event_type='updated'", original.Properties.ID).Scan(&writerEvents); err != nil || afterEvents != beforeEvents+1 || writerEvents != 1 {
		t.Fatalf("due write replayed or lost audit: before=%d after=%d writer=%d err=%v", beforeEvents, afterEvents, writerEvents, err)
	}
	for path, want := range map[string]any{"beads/work": current, "beads/prereq": target, "links/block": dependency} {
		if actual, err := direct.Read(ctx, path); err != nil || !reflect.DeepEqual(actual, want) {
			t.Fatalf("lost-response write changed complete %s: %v", path, err)
		}
	}
}

func TestIssueDueCreateRetentionRollback(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, s := createFieldsStore(t, backend)
			original, err := s.CreateIssue(ctx, "beads/existing", initialNotesRequest("Existing context"))
			if err != nil {
				t.Fatal(err)
			}
			state := createFieldsState(t, ctx, s)
			request := initialNotesRequest("Due create must roll back")
			input := time.Date(2037, 6, 7, 8, 9, 10, 500000000, time.FixedZone("west", -4*3600))
			request.Issue.DueAt = &input
			want := time.Date(2037, 6, 7, 12, 9, 11, 0, time.UTC)
			fault := errors.New("failure after initial due snapshot and graph mapping")
			s.afterWrite = func(stage string) error {
				if stage == "retained" {
					return fault
				}
				return nil
			}
			got, err := s.CreateIssue(ctx, "beads/due", request)
			s.afterWrite = nil
			if !errors.Is(err, fault) || !reflect.DeepEqual(got, IssueRecord{}) || !reflect.DeepEqual(state, createFieldsState(t, ctx, s)) {
				t.Fatalf("due create retained rollback leaked state: %v", err)
			}
			if current, err := s.Read(ctx, "beads/existing"); err != nil || !reflect.DeepEqual(current, original) {
				t.Fatalf("failed create changed existing Issue: %v", err)
			}
			assertIssueEditCounts(t, ctx, s, original.Properties.ID, 1)
			assertIssueEditVersion(t, ctx, s, "beads/existing", original)
			// The failed allocation is reusable. The successful attempt retains the
			// normalized value in its first snapshot, with no synthetic update.
			created, err := s.CreateIssue(ctx, "beads/due", request)
			if err != nil {
				t.Fatal(err)
			}
			assertInitialNotes(t, created, request)
			if !reflect.DeepEqual(created.Properties.DueAt, &want) {
				t.Fatal("successful create lost rounded UTC due value")
			}
			assertIssueEditCounts(t, ctx, s, created.Properties.ID, 1)
			assertIssueEditVersion(t, ctx, s, "beads/due", created)
		})
	}
}
