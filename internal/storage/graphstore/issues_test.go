//go:build cgo

package graphstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

func issueExperimentOptions(t *testing.T, backend string) (context.Context, Options) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	o := testOptions(t)
	o.IssuePrefix = "exp"
	if backend == "server" {
		port := os.Getenv("BEADS_GRAPH_TEST_SERVER_PORT")
		if port == "" {
			t.Skip("set BEADS_GRAPH_TEST_SERVER_PORT for released-server Issue adapter checks")
		}
		var err error
		o.ServerPort, err = strconv.Atoi(port)
		if err != nil {
			t.Fatal(err)
		}
		token, err := freshToken()
		if err != nil {
			t.Fatal(err)
		}
		o.Backend, o.ServerHost, o.ServerUser, o.Database = "server", "127.0.0.1", "root", "graph_issue_"+token
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
	}
	if err := Init(ctx, o); err != nil {
		t.Fatal(err)
	}
	return ctx, o
}

func plainIssue(title string) publicops.CreateRequest {
	return publicops.CreateRequest{Actor: "test-author", Issue: &publicops.Issue{
		Title: title, Description: "A durable Issue — 記憶", Status: types.StatusOpen,
		IssueType: types.TypeTask, Priority: 2, Labels: []string{"demo", "graph"},
	}}
}

func TestIssueAdapterCreateReadReopen(t *testing.T) {
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
			fault := errors.New("injected Issue adapter failure")
			for _, stage := range []string{"issue", "issue-catalog", "retained"} {
				t.Run("rollback-"+stage, func(t *testing.T) {
					var before string
					if err := s.db.QueryRowContext(ctx, `SELECT writer_token FROM graph_preview_scope WHERE singleton=1`).Scan(&before); err != nil {
						t.Fatal(err)
					}
					s.afterWrite = func(at string) error {
						if at == stage {
							return fault
						}
						return nil
					}
					_, err := s.CreateIssue(ctx, "beads/rollback", plainIssue("rolled back"))
					s.afterWrite = nil
					if !errors.Is(err, fault) {
						t.Fatalf("want injected failure, got %v", err)
					}
					for _, table := range []string{"issues", "labels", "events", "issue_versions", "store_epoch", "graph_preview_catalog", "graph_preview_issue_versions"} {
						var count int
						if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
							t.Fatal(err)
						}
						if count != 0 {
							t.Fatalf("rollback left %d rows in %s", count, table)
						}
					}
					var after string
					if err := s.db.QueryRowContext(ctx, `SELECT writer_token FROM graph_preview_scope WHERE singleton=1`).Scan(&after); err != nil {
						t.Fatal(err)
					}
					if before != after {
						t.Fatal("coordination token escaped rollback")
					}
				})
			}
			req := plainIssue("First graph Issue")
			created, err := s.CreateIssue(ctx, "beads/task", req)
			if err != nil {
				t.Fatal(err)
			}
			if req.Issue.ID != "" {
				t.Fatal("adapter mutated caller's request")
			}
			if created.ID != o.Binding.ScopeURL+"beads/task" || created.Type != IssueTypeURL(o.Binding.ScopeURL) || !strings.HasPrefix(created.Properties.ID, "exp-") || created.Owned == nil {
				t.Fatalf("wrong record: %+v", created)
			}
			if _, err := s.CreateIssue(ctx, "beads/task", plainIssue("duplicate path")); !errors.Is(err, ErrAlreadyExists) {
				t.Fatalf("duplicate path: %v", err)
			}
			shown, err := s.ShowIssue(ctx, "beads/task")
			if err != nil || !reflect.DeepEqual(shown, created) {
				t.Fatalf("shown differs: %+v err=%v", shown, err)
			}
			generic, err := s.Read(ctx, "beads/task")
			if err != nil || !reflect.DeepEqual(generic, created) {
				t.Fatalf("generic differs: %+v err=%v", generic, err)
			}
			if err := s.withTx(ctx, false, func(tx *sql.Tx) error {
				hydrated, err := issueops.HydrateIssueOperationResult(ctx, tx, created.Properties.ID, true)
				if err != nil {
					return err
				}
				if !reflect.DeepEqual(hydrated, created.Properties) {
					t.Error("graph and Issue domain read differ")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			var ordinal, current int64
			var snapshot []byte
			if err := s.db.QueryRowContext(ctx, `SELECT v.revision,v.durable_state,i.current_revision FROM issue_versions v JOIN issues i ON i.id=v.issue_id WHERE i.id=?`, created.Properties.ID).Scan(&ordinal, &snapshot, &current); err != nil {
				t.Fatal(err)
			}
			canonical, err := canonicalJSON(created.Properties)
			if err != nil {
				t.Fatal(err)
			}
			if ordinal != 1 || current != 1 || !bytes.Equal(snapshot, canonical) {
				t.Fatalf("retained bytes differ: ordinal=%d current=%d snapshot=%s properties=%s", ordinal, current, snapshot, canonical)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(snapshot, &fields); err != nil {
				t.Fatal(err)
			}
			keys := make([]string, 0, len(fields))
			for key := range fields {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			_, hasOrdinal := fields["current_revision"]
			t.Logf("complete retained Issue JSON fields: %v; current_revision present=%v", keys, hasOrdinal)
			for _, table := range []string{"issues", "issue_versions", "graph_preview_catalog", "graph_preview_issue_versions"} {
				var n int
				if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n != 1 {
					t.Fatalf("%s count=%d, want one", table, n)
				}
			}
			for _, table := range []string{"graph_preview_payloads", "graph_preview_versions"} {
				var n int
				if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n != 0 {
					t.Fatalf("second Issue copy in %s", table)
				}
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = OpenExisting(ctx, o)
			if err != nil {
				t.Fatal(err)
			}
			shown, err = s.ShowIssue(ctx, "beads/task")
			if err != nil || !reflect.DeepEqual(shown, created) {
				t.Fatalf("reopen differs: %+v err=%v", shown, err)
			}
			memory, err := s.Create(ctx, CreateRequest{Path: "beads/memory", Title: "Memory", Body: "same database"})
			if err != nil {
				t.Fatal(err)
			}
			readMemory, err := s.Read(ctx, "beads/memory")
			if err != nil || !reflect.DeepEqual(readMemory, memory) {
				t.Fatalf("mixed read differs: %+v err=%v", readMemory, err)
			}
		})
	}
}

func TestIssueAdapterRefusesUnsupportedRequests(t *testing.T) {
	ctx, o := issueExperimentOptions(t, "embedded")
	s, err := OpenExisting(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	for _, tc := range []struct {
		name   string
		mutate func(*publicops.CreateRequest)
	}{
		{"ephemeral", func(r *publicops.CreateRequest) { r.Issue.Ephemeral = true }},
		{"no-history", func(r *publicops.CreateRequest) { r.Issue.NoHistory = true }},
		{"parent", func(r *publicops.CreateRequest) { r.ParentID = "exp-parent" }},
		{"metadata-nonobject", func(r *publicops.CreateRequest) { r.Issue.Metadata = json.RawMessage(`[]`) }},
		{"infra", func(r *publicops.CreateRequest) { r.Issue.IssueType = "agent" }},
		{"wrong-prefix", func(r *publicops.CreateRequest) { r.Issue.ID = "other-123" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := plainIssue(tc.name)
			tc.mutate(&r)
			if _, err := s.CreateIssue(ctx, "beads/refused", r); !errors.Is(err, storage.ErrValidation) {
				t.Fatalf("refusal: %v", err)
			}
		})
	}
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("unsupported requests created %d Issues", count)
	}
}

func TestIssueAndMemoryCompete(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, o := issueExperimentOptions(t, backend)
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			reached := make(chan struct{}, 2)
			release := make(chan struct{})
			barrier := func(stage string) error {
				if backend != "server" || stage != "retained" {
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
			type result struct {
				issue bool
				err   error
			}
			results := make(chan result, 2)
			for _, isIssue := range []bool{true, false} {
				go func() {
					s, err := OpenExisting(ctx, o)
					if err != nil {
						results <- result{isIssue, err}
						return
					}
					s.afterWrite = barrier
					if isIssue {
						_, err = s.CreateIssue(ctx, "beads/race-issue", plainIssue("racing Issue"))
					} else {
						_, err = s.Create(ctx, CreateRequest{Path: "beads/race-memory", Title: "racing Memory"})
					}
					err = errors.Join(err, s.Close())
					results <- result{isIssue, err}
				}()
			}
			if backend == "server" {
				for range 2 {
					select {
					case <-reached:
					case early := <-results:
						t.Fatalf("writer returned before overlap barrier: Issue=%v outcome=%v", early.issue, early.err)
					case <-ctx.Done():
						t.Fatal("writers did not reach barrier", ctx.Err())
					}
				}
				close(release)
			}
			accepted := map[bool]bool{}
			conflicts := 0
			for range 2 {
				r := <-results
				t.Logf("Issue=%v outcome=%v", r.issue, r.err)
				if r.err == nil {
					accepted[r.issue] = true
				} else if errors.Is(r.err, ErrConflict) && !errors.Is(r.err, ErrOutcomeUnknown) {
					conflicts++
				} else {
					t.Fatal(r.err)
				}
			}
			if backend == "server" && (len(accepted) != 1 || conflicts != 1) {
				t.Fatalf("accepted=%v conflicts=%d", accepted, conflicts)
			}
			if backend == "embedded" && (len(accepted) != 2 || conflicts != 0) {
				t.Fatalf("accepted=%v conflicts=%d", accepted, conflicts)
			}
			s, err := OpenExisting(ctx, o)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := s.Close(); err != nil {
					t.Error(err)
				}
			}()
			for isIssue, path := range map[bool]string{true: "beads/race-issue", false: "beads/race-memory"} {
				_, err := s.Read(ctx, path)
				if accepted[isIssue] && err != nil {
					t.Fatal(err)
				}
				if !accepted[isIssue] && !errors.Is(err, ErrNotFound) {
					t.Fatalf("loser visible: %v", err)
				}
			}
			wantIssue := 0
			if accepted[true] {
				wantIssue = 1
			}
			for _, table := range []string{"issues", "issue_versions", "graph_preview_issue_versions"} {
				var n int
				if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n != wantIssue {
					t.Fatalf("%s rows=%d want=%d", table, n, wantIssue)
				}
			}
		})
	}
}
