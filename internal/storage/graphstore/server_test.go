//go:build cgo

package graphstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
)

// This opt-in test uses a released sql-server supplied by the caller. It owns
// only its fresh database and never stops/reconfigures the shared server.
func TestServerConcurrentRollback(t *testing.T) {
	portText := os.Getenv("BEADS_GRAPH_TEST_SERVER_PORT")
	if portText == "" {
		t.Skip("set BEADS_GRAPH_TEST_SERVER_PORT for ordinary released-server qualification")
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	o := testOptions(t)
	token, err := freshToken()
	if err != nil {
		t.Fatal(err)
	}
	o.Backend = "server"
	o.Database = "graph_test_" + token
	o.ServerHost = "127.0.0.1"
	o.ServerPort = port
	o.ServerUser = "root"
	if err := Init(ctx, o); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		admin, err := openBackend(cleanupCtx, o, "")
		if err != nil {
			t.Error(err)
			return
		}
		if _, err = admin.db.ExecContext(cleanupCtx, "DROP DATABASE `"+o.Database+"`"); err != nil {
			t.Error(err)
		}
		if err = admin.Close(); err != nil {
			t.Error(err)
		}
	})
	left, err := OpenExisting(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := left.Close(); err != nil {
			t.Error(err)
		}
	})
	runRollbackControls(t, ctx, left)
	right, err := OpenExisting(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := right.Close(); err != nil {
			t.Error(err)
		}
	})
	// Both transactions have written allocation/current/retained state from
	// overlapping snapshots before either is allowed to publish.
	reached := make(chan struct{}, 2)
	release := make(chan struct{})
	barrier := func(stage string) error {
		if stage != "retained" {
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
	left.afterWrite = barrier
	right.afterWrite = barrier
	type result struct {
		path string
		err  error
	}
	results := make(chan result, 2)
	go func() {
		_, err := left.Create(ctx, CreateRequest{Path: "beads/left", Title: "left"})
		results <- result{"beads/left", err}
	}()
	go func() {
		_, err := right.Create(ctx, CreateRequest{Path: "beads/right", Title: "right"})
		results <- result{"beads/right", err}
	}()
	for range 2 {
		select {
		case <-reached:
		case <-ctx.Done():
			t.Fatal("concurrent writers did not reach barrier:", ctx.Err())
		}
	}
	close(release)
	var winner, loser string
	for range 2 {
		r := <-results
		t.Logf("%s: %v", r.path, r.err)
		switch {
		case r.err == nil:
			if winner != "" {
				t.Fatal("both overlapping writers committed")
			}
			winner = r.path
		case errors.Is(r.err, ErrConflict) && !errors.Is(r.err, ErrOutcomeUnknown):
			if loser != "" {
				t.Fatal("both writers conflicted")
			}
			loser = r.path
		default:
			t.Fatalf("unclassified result: %v", r.err)
		}
	}
	left.afterWrite = nil
	right.afterWrite = nil
	if winner == "" || loser == "" {
		t.Fatalf("winner=%q loser=%q", winner, loser)
	}
	if _, err := left.Show(ctx, winner); err != nil {
		t.Fatal(err)
	}
	if _, err := left.Show(ctx, loser); !errors.Is(err, ErrNotFound) {
		t.Fatalf("loser published: %v", err)
	}
	for _, table := range []string{"graph_preview_catalog", "graph_preview_payloads", "graph_preview_versions"} {
		var count int
		if err := left.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE path = ?", loser).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("loser has %d partial rows in %s", count, table)
		}
	}
	// Cancellation after all writes and before COMMIT cannot be reported as a
	// success. A fresh transaction accounts for all tables before a successor.
	cancelCtx, abort := context.WithCancel(ctx)
	left.afterWrite = func(stage string) error {
		if stage == "retained" {
			abort()
			return cancelCtx.Err()
		}
		return nil
	}
	_, err = left.Create(cancelCtx, CreateRequest{Path: "beads/cancelled"})
	left.afterWrite = nil
	abort()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled write: %v", err)
	}
	if err := left.Close(); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"graph_preview_catalog", "graph_preview_payloads", "graph_preview_versions"} {
		var count int
		if err := right.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE path = 'beads/cancelled'").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("cancelled transaction has %d rows in %s", count, table)
		}
	}
	if _, err := right.Create(ctx, CreateRequest{Path: "beads/successor"}); err != nil {
		t.Fatal(err)
	}
}

func TestCommitErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input error
		want  error
	}{
		{"confirmed rollback", &mysql.MySQLError{Number: 1213, SQLState: [5]byte{'4', '0', '0', '0', '1'}}, ErrConflict},
		{"code without state", &mysql.MySQLError{Number: 1213}, ErrOutcomeUnknown},
		{"lock timeout", &mysql.MySQLError{Number: 1205}, ErrOutcomeUnknown},
		{"wire loss", fmt.Errorf("connection lost after COMMIT"), ErrOutcomeUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := classifyCommitError(tc.input)
			if !errors.Is(err, tc.want) || !errors.Is(err, tc.input) {
				t.Fatalf("classified %v", err)
			}
			if tc.want == ErrConflict && errors.Is(err, ErrOutcomeUnknown) {
				t.Fatal("confirmed rollback labeled unknown")
			}
		})
	}
}
