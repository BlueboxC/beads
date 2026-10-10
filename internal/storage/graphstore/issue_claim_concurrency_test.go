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
	"time"

	"github.com/steveyegge/beads/internal/storage"
)

func TestIssueClaimConcurrentWriters(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			base, options, first, source, target, dependency := reopenFixture(t, backend)
			ctx, cancel := context.WithTimeout(base, time.Minute)
			defer cancel()
			// Embedded retains its supported one-session connector. Only ordinary
			// server forces overlap between independent checked transactions.
			second := &Store{db: first.db, options: options}
			if backend == "server" {
				var err error
				second, err = OpenExisting(ctx, options)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := second.Close(); err != nil {
						t.Error(err)
					}
				})
			}
			var beforeEvents int
			if err := first.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE issue_id=?", source.Properties.ID).Scan(&beforeEvents); err != nil {
				t.Fatal(err)
			}
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
			type outcome struct {
				actor string
				value IssueMutationResult
				err   error
			}
			results := make(chan outcome, 2)
			var writers sync.WaitGroup
			writers.Add(2)
			defer func() { cancel(); writers.Wait(); first.afterWrite, second.afterWrite = nil, nil }()
			for i, s := range []*Store{first, second} {
				actor := []string{"first-claimant", "second-claimant"}[i]
				go func() {
					defer writers.Done()
					value, err := s.ClaimIssue(ctx, "beads/work", actor)
					results <- outcome{actor, value, err}
				}()
			}
			if backend == "server" {
				for range 2 {
					select {
					case <-reached:
					case result := <-results:
						t.Fatalf("claim exited before forced overlap: %+v", result)
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				}
				close(release)
			}
			var winner outcome
			refusals := 0
			for range 2 {
				select {
				case result := <-results:
					if result.err == nil {
						if winner.actor != "" || !result.value.Changed {
							t.Fatalf("not one changed claimant: %+v", result)
						}
						winner = result
					} else {
						// The server loser conflicts at COMMIT; the serialized embedded
						// loser observes the winner's holder and is refused by the CAS.
						want := storage.ErrAlreadyClaimed
						if backend == "server" {
							want = ErrConflict
						}
						if !errors.Is(result.err, want) || errors.Is(result.err, ErrOutcomeUnknown) || !reflect.DeepEqual(result.value, IssueMutationResult{}) {
							t.Fatalf("bad losing outcome: %+v", result)
						}
						refusals++
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			writers.Wait()
			first.afterWrite, second.afterWrite = nil, nil
			if winner.actor == "" || refusals != 1 {
				t.Fatalf("winner=%s refusals%d", winner.actor, refusals)
			}
			current, err := first.ShowIssue(ctx, "beads/work")
			if err != nil || !reflect.DeepEqual(current, winner.value.Issue) {
				t.Fatalf("not complete winner: %+v %v", current, err)
			}
			assertClaimRecord(t, source, current, winner.actor)
			assertClaimLease(t, ctx, first, source.Properties.ID, winner.actor)
			assertClaimEvents(t, ctx, first, source.Properties.ID, winner.actor, 1)
			loser := "first-claimant"
			if winner.actor == loser {
				loser = "second-claimant"
			}
			var afterEvents, loserEvents int
			if err := first.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE issue_id=?", source.Properties.ID).Scan(&afterEvents); err != nil {
				t.Fatal(err)
			}
			if err := first.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE issue_id=? AND actor=?", source.Properties.ID, loser).Scan(&loserEvents); err != nil {
				t.Fatal(err)
			}
			if afterEvents != beforeEvents+1 || loserEvents != 0 {
				t.Fatalf("claim audit leaked: before%d after%d loser%d", beforeEvents, afterEvents, loserEvents)
			}
			assertIssueEditCounts(t, ctx, first, source.Properties.ID, 3)
			assertIssueEditVersion(t, ctx, first, "beads/work", source)
			assertIssueEditVersion(t, ctx, first, "beads/work", current)
			assertReadyIDs(t, ctx, first, target.ID)
			if got, err := first.ShowIssue(ctx, "beads/prereq"); err != nil || !reflect.DeepEqual(got, target) {
				t.Fatalf("target changed: %+v %v", got, err)
			}
			if got, err := first.ShowLink(ctx, "links/block"); err != nil || !reflect.DeepEqual(got, dependency) {
				t.Fatalf("Dependency changed: %+v %v", got, err)
			}
		})
	}
}

func TestIssueClaimLostCommitResponse(t *testing.T) {
	ctx, options, direct, source, target, dependency := reopenFixture(t, "server")
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
	got, err := s.ClaimIssue(ctx, "beads/work", "commit-lost-claimant")
	if !errors.Is(err, ErrOutcomeUnknown) || errors.Is(err, ErrConflict) || !reflect.DeepEqual(got, IssueMutationResult{}) {
		t.Fatalf("lost reply falsely classified: %+v %v", got, err)
	}
	select {
	case packet := <-observed:
		if len(packet) == 0 || packet[0] != 0 {
			t.Fatalf("no successful COMMIT witness: %x", packet)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// The private proxy saw COMMIT success, but the writer did not. Only an
	// independent read can establish the outcome; there is no automatic replay.
	current, err := direct.ShowIssue(ctx, "beads/work")
	if err != nil {
		t.Fatal(err)
	}
	assertClaimRecord(t, source, current, "commit-lost-claimant")
	assertClaimLease(t, ctx, direct, source.Properties.ID, "commit-lost-claimant")
	assertClaimEvents(t, ctx, direct, source.Properties.ID, "commit-lost-claimant", 1)
	assertIssueEditCounts(t, ctx, direct, source.Properties.ID, 3)
	assertIssueEditVersion(t, ctx, direct, "beads/work", source)
	assertIssueEditVersion(t, ctx, direct, "beads/work", current)
	if got, err := direct.ShowIssue(ctx, "beads/prereq"); err != nil || !reflect.DeepEqual(got, target) {
		t.Fatalf("target changed: %+v %v", got, err)
	}
	if got, err := direct.ShowLink(ctx, "links/block"); err != nil || !reflect.DeepEqual(got, dependency) {
		t.Fatalf("Dependency changed: %+v %v", got, err)
	}
}
