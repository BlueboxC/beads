//go:build cgo

package graphstore

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

func issueListInt(value int) *int { return &value }

func issueListFixture(t *testing.T, backend string) (context.Context, Options, *Store) {
	t.Helper()
	ctx, options := issueExperimentOptions(t, backend)
	s, err := OpenExisting(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return ctx, options, s
}

// Live Issue row-lock tokens and content hashes are deliberately absent from
// retained JSON (see ReadVersion and assertIssueEditVersion). Compare every
// serialized field, including complete owned Links, without requiring equality
// of those current-row-only Go fields or decoded time representations.
func assertIssueListRetained(t *testing.T, ctx context.Context, s *Store, path string, want IssueRecord) {
	t.Helper()
	got, err := s.ReadVersion(ctx, path, want.Version)
	if err != nil {
		t.Fatal(err)
	}
	gotJSON, gotErr := canonicalJSON(got)
	wantJSON, wantErr := canonicalJSON(want)
	if gotErr != nil || wantErr != nil || !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("retained complete JSON differs: got=%s want=%s errors=%v/%v", gotJSON, wantJSON, gotErr, wantErr)
	}
}

func TestGraphIssueListRequestAdmission(t *testing.T) {
	// Every nonzero unsupported request field must refuse before touching a DB.
	admitted := map[string]bool{}
	for _, name := range []string{"Status", "IssueType", "TitleSearch", "TitleContains", "Assignee", "NoAssignee", "DueBefore", "DueAfter", "OverdueFlag", "Labels", "LabelsAny", "ExcludeLabels", "Priority", "PriorityMin", "PriorityMax", "PinnedFlag", "NoPinnedFlag", "AllFlag", "SortBy", "Reverse", "Limit", "MaxRows", "MaxRowsSource"} {
		admitted[name] = true
	}
	typ := reflect.TypeFor[publicops.ListRequest]()
	var store *Store
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if admitted[field.Name] {
			continue
		}
		t.Run(field.Name, func(t *testing.T) {
			request := publicops.ListRequest{}
			value := reflect.ValueOf(&request).Elem().Field(i)
			switch value.Kind() {
			case reflect.Bool:
				value.SetBool(true)
			case reflect.String:
				value.SetString("unsupported")
			case reflect.Int:
				value.SetInt(1)
			case reflect.Pointer:
				value.Set(reflect.New(value.Type().Elem()))
			case reflect.Slice:
				value.Set(reflect.MakeSlice(value.Type(), 0, 0))
			case reflect.Map:
				value.Set(reflect.MakeMap(value.Type()))
			default:
				t.Fatalf("add admission test for field kind %s", value.Kind())
			}
			got, err := store.ListIssues(context.Background(), request)
			if !errors.Is(err, ErrCapabilityUnavailable) || !reflect.ValueOf(got).IsZero() {
				t.Fatalf("admitted unsupported field: %+v %v", got, err)
			}
		})
	}
	for _, request := range []publicops.ListRequest{
		{SortBy: "id"}, {SortBy: "assignee"}, {SortBy: "closed"}, {SortBy: "bad"},
	} {
		got, err := store.ListIssues(context.Background(), request)
		if !errors.Is(err, ErrCapabilityUnavailable) || !reflect.ValueOf(got).IsZero() {
			t.Fatalf("sort: %+v %v", got, err)
		}
	}
	for _, request := range []publicops.ListRequest{
		{PinnedFlag: true, NoPinnedFlag: true}, {Limit: issueListInt(-1)}, {MaxRows: -1}, {Priority: issueListInt(5)}, {PriorityMin: issueListInt(-1)}, {PriorityMax: issueListInt(5)},
		{Status: string([]byte{255})}, {Labels: []string{string([]byte{255})}},
		{TitleSearch: strings.Repeat("x", PreviewIssueListRequestByteLimit+1)}, {Labels: make([]string, PreviewIssueListLabelLimit+1)},
		{TitleSearch: strings.Repeat("x", PreviewIssueListRequestByteLimit), Labels: []string{"overflow"}},
	} {
		got, err := store.ListIssues(context.Background(), request)
		if !errors.Is(err, storage.ErrValidation) || !reflect.ValueOf(got).IsZero() {
			t.Fatalf("validation: %+v %v", got, err)
		}
	}
	limit, priority := 3, 1
	labels := []string{"before"}
	copied, err := prepareIssueListRequest(publicops.ListRequest{Limit: &limit, Priority: &priority, Labels: labels})
	if err != nil {
		t.Fatal(err)
	}
	limit, priority, labels[0] = 99, 4, "after"
	if *copied.Limit != 3 || *copied.Priority != 1 || copied.Labels[0] != "before" {
		t.Fatalf("caller-owned request retained: %+v", copied)
	}
}

func TestGraphIssueListLifecycle(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, options, s := issueListFixture(t, backend)
			empty, err := s.ListIssues(ctx, publicops.ListRequest{})
			if err != nil || empty.Items == nil || len(empty.Items) != 0 || empty.HasMore {
				t.Fatalf("empty: %+v %v", empty, err)
			}
			memory, err := s.Create(ctx, CreateRequest{Path: "beads/plan", Body: "Issue context"})
			if err != nil {
				t.Fatal(err)
			}
			wanted := map[string]IssueRecord{}
			for _, entry := range []struct {
				path, title string
				priority    int
				labels      []string
			}{
				{"beads/work", "Alpha work", 1, []string{"shared", "work"}}, {"beads/prereq", "Beta prerequisite", 2, []string{"shared", "prerequisite"}}, {"beads/other", "Gamma other", 3, []string{"other"}},
			} {
				request := plainIssue(entry.title)
				request.Issue.Priority = entry.priority
				request.Issue.Labels = entry.labels
				got, err := s.CreateIssue(ctx, entry.path, request)
				if err != nil {
					t.Fatal(err)
				}
				wanted[entry.path] = got
			}
			dep, err := s.AddDependency(ctx, DependencyRequest{Path: "links/block", SourcePath: "beads/work", TargetPath: "beads/prereq", Actor: "author"})
			if err != nil {
				t.Fatal(err)
			}
			wanted["beads/work"] = dep.Source
			if _, err := s.AddInformationalLink(ctx, LinkCreateRequest{Path: "links/context", SourcePath: "beads/plan", TargetPath: "beads/work", ExpectedSourceRevision: memory.Revision}); err != nil {
				t.Fatal(err)
			}
			before := reopenState(t, ctx, s)
			page, err := s.ListIssues(ctx, publicops.ListRequest{Limit: issueListInt(0)})
			if err != nil || page.HasMore || len(page.Items) != 3 {
				t.Fatalf("list: %+v %v", page, err)
			}
			for i, path := range []string{"beads/work", "beads/prereq", "beads/other"} {
				if !reflect.DeepEqual(page.Items[i], wanted[path]) {
					t.Fatalf("canonical complete record %s: %+v", path, page.Items[i])
				}
				assertIssueListRetained(t, ctx, s, path, page.Items[i])
			}
			ready, err := s.ReadyIssues(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, record := range ready {
				if record.ID == dep.Source.ID {
					t.Fatal("blocked source is ready")
				}
			}
			if !reflect.DeepEqual(before, reopenState(t, ctx, s)) {
				t.Fatal("listing/readiness changed stored state")
			}
			for _, tc := range []struct {
				name    string
				request publicops.ListRequest
				paths   []string
				more    bool
			}{
				{"one", publicops.ListRequest{Limit: issueListInt(1)}, []string{"beads/work"}, true},
				{"all-explicit-one", publicops.ListRequest{AllFlag: true, Limit: issueListInt(1)}, []string{"beads/work"}, true},
				{"exact-page", publicops.ListRequest{Limit: issueListInt(3)}, []string{"beads/work", "beads/prereq", "beads/other"}, false},
				{"huge-page", publicops.ListRequest{Limit: issueListInt(int(^uint(0) >> 1))}, []string{"beads/work", "beads/prereq", "beads/other"}, false},
				{"title", publicops.ListRequest{TitleSearch: "Alpha"}, []string{"beads/work"}, false},
				{"contains", publicops.ListRequest{TitleContains: "prereq"}, []string{"beads/prereq"}, false},
				{"labels", publicops.ListRequest{Labels: []string{"shared", "work"}}, []string{"beads/work"}, false},
				{"labels-any", publicops.ListRequest{LabelsAny: []string{"work", "other"}}, []string{"beads/work", "beads/other"}, false},
				{"exclude", publicops.ListRequest{ExcludeLabels: []string{"shared"}}, []string{"beads/other"}, false},
				{"priority", publicops.ListRequest{Priority: issueListInt(2)}, []string{"beads/prereq"}, false},
				{"range", publicops.ListRequest{PriorityMin: issueListInt(2), PriorityMax: issueListInt(3)}, []string{"beads/prereq", "beads/other"}, false},
				{"reverse", publicops.ListRequest{SortBy: "title", Reverse: true}, []string{"beads/other", "beads/prereq", "beads/work"}, false},
				{"empty", publicops.ListRequest{TitleContains: "absent"}, []string{}, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					got, err := s.ListIssues(ctx, tc.request)
					if err != nil || got.Items == nil || len(got.Items) != len(tc.paths) || got.HasMore != tc.more {
						t.Fatalf("page: %+v %v", got, err)
					}
					for i, path := range tc.paths {
						if !reflect.DeepEqual(got.Items[i], wanted[path]) {
							t.Fatalf("wrong row %s: %+v", path, got.Items[i])
						}
					}
				})
			}
			if !reflect.DeepEqual(before, reopenState(t, ctx, s)) {
				t.Fatal("filters/pages changed stored state")
			}
			if got, err := s.ListIssues(ctx, publicops.ListRequest{Limit: issueListInt(0), MaxRows: 1, MaxRowsSource: "test"}); !errors.Is(err, ErrLimitExceeded) || !reflect.ValueOf(got).IsZero() {
				t.Fatalf("row cap: %+v %v", got, err)
			}
			closed, err := s.CloseIssue(ctx, "beads/prereq", "done", "closer")
			if err != nil {
				t.Fatal(err)
			}
			page, err = s.ListIssues(ctx, publicops.ListRequest{})
			if err != nil || len(page.Items) != 2 {
				t.Fatalf("closed default: %+v %v", page, err)
			}
			all, err := s.ListIssues(ctx, publicops.ListRequest{AllFlag: true, Limit: issueListInt(0)})
			if err != nil || len(all.Items) != 3 || !reflect.DeepEqual(all.Items[1], closed.Issue) {
				t.Fatalf("all closed: %+v %v", all, err)
			}
			reopened, err := s.ReopenIssue(ctx, "beads/prereq", "again", "reopener")
			if err != nil {
				t.Fatal(err)
			}
			page, err = s.ListIssues(ctx, publicops.ListRequest{Status: "open", TitleContains: "prereq"})
			if err != nil || len(page.Items) != 1 || !reflect.DeepEqual(page.Items[0], reopened.Issue) {
				t.Fatalf("reopened: %+v %v", page, err)
			}
			// Fresh handles resolve the same accepted identities and versions.
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			second, err := OpenExisting(ctx, options)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := second.Close(); err != nil {
					t.Error(err)
				}
			}()
			got, err := second.ListIssues(ctx, publicops.ListRequest{Status: "open", TitleContains: "prereq"})
			if err != nil || !reflect.DeepEqual(got, page) {
				t.Fatalf("reopened store: %+v %v", got, err)
			}
		})
	}
}

func setIssueListConfig(t *testing.T, ctx context.Context, s *Store, key, value string) {
	t.Helper()
	// Existing transaction-aware configuration APIs; no raw Issue/snapshot
	// fixtures. Graph CLI has no configuration setter, so this is storage proof.
	if err := s.withTx(ctx, true, func(tx *sql.Tx) error {
		if err := issueops.SetConfigInTx(ctx, tx, key, value); err != nil {
			return err
		}
		_, err := issueops.SyncConfigTables(ctx, tx, key, value)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestGraphIssueListConfiguredPolicy(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s := issueListFixture(t, backend)
			setIssueListConfig(t, ctx, s, "status.custom", "triaged:active,archived:done,on-ice:frozen")
			for _, status := range []types.Status{types.StatusOpen, types.StatusClosed, types.StatusPinned, "triaged", "archived", "on-ice"} {
				request := plainIssue(string(status))
				request.Issue.Status = status
				if _, err := s.CreateIssue(ctx, "beads/"+string(status), request); err != nil {
					t.Fatal(err)
				}
			}
			for _, tc := range []struct {
				request  publicops.ListRequest
				statuses []types.Status
			}{
				{publicops.ListRequest{}, []types.Status{types.StatusOpen, "triaged"}},
				{publicops.ListRequest{AllFlag: true, Limit: issueListInt(0)}, []types.Status{types.StatusOpen, types.StatusClosed, types.StatusPinned, "triaged", "archived", "on-ice"}},
				{publicops.ListRequest{Status: "archived,on-ice"}, []types.Status{"archived", "on-ice"}},
				{publicops.ListRequest{Status: "pinned"}, []types.Status{types.StatusPinned}},
			} {
				got, err := s.ListIssues(ctx, tc.request)
				if err != nil || len(got.Items) != len(tc.statuses) {
					t.Fatalf("categories: %+v %v", got, err)
				}
				expected := map[types.Status]bool{}
				for _, status := range tc.statuses {
					expected[status] = true
				}
				for _, row := range got.Items {
					if !expected[row.Properties.Status] {
						t.Fatalf("unexpected category %s", row.Properties.Status)
					}
					delete(expected, row.Properties.Status)
				}
				if len(expected) != 0 {
					t.Fatalf("missing categories: %v", expected)
				}
			}
			// Gate and custom Issue classifications are already directly
			// authorable. Do not widen the writer to fabricate template or
			// expired-defer fixtures that this graph preview cannot author.
			setIssueListConfig(t, ctx, s, "types.custom", "research")
			typeRecords := map[string]IssueRecord{}
			for _, classification := range []string{"gate", "research"} {
				request := plainIssue("Type policy " + classification)
				request.Issue.IssueType = types.IssueType(classification)
				record, err := s.CreateIssue(ctx, "beads/type-"+classification, request)
				if err != nil {
					t.Fatal(err)
				}
				typeRecords[classification] = record
			}
			typeState := reopenState(t, ctx, s)
			for _, all := range []bool{false, true} {
				got, err := s.ListIssues(ctx, publicops.ListRequest{TitleContains: "Type policy", AllFlag: all})
				if err != nil || len(got.Items) != 1 || !reflect.DeepEqual(got.Items[0], typeRecords["research"]) {
					t.Fatalf("default gate suppression/custom type: %+v %v", got, err)
				}
			}
			for _, classification := range []string{"gate", "research"} {
				got, err := s.ListIssues(ctx, publicops.ListRequest{IssueType: classification})
				if err != nil || len(got.Items) != 1 || !reflect.DeepEqual(got.Items[0], typeRecords[classification]) {
					t.Fatalf("explicit type %s: %+v %v", classification, got, err)
				}
				assertIssueListRetained(t, ctx, s, "beads/type-"+classification, got.Items[0])
			}
			if !reflect.DeepEqual(typeState, reopenState(t, ctx, s)) {
				t.Fatal("type selection changed state")
			}
			// Reclassifying task as infrastructure uses existing configuration and
			// makes the default hide it. --all must not lift Type-plane suppression.
			setIssueListConfig(t, ctx, s, "types.infra", "task,research")
			before := reopenState(t, ctx, s)
			for _, request := range []publicops.ListRequest{{}, {AllFlag: true}} {
				got, err := s.ListIssues(ctx, request)
				if err != nil || got.Items == nil || len(got.Items) != 0 || got.HasMore {
					t.Fatalf("infra suppression: %+v %v", got, err)
				}
			}
			if got, err := s.ListIssues(ctx, publicops.ListRequest{IssueType: "task"}); !errors.Is(err, ErrCapabilityUnavailable) || !reflect.ValueOf(got).IsZero() {
				t.Fatalf("explicit infra: %+v %v", got, err)
			}
			if !reflect.DeepEqual(before, reopenState(t, ctx, s)) {
				t.Fatal("configuration-aware read mutated state")
			}
		})
	}
}

func TestGraphIssueListRefusals(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s := issueListFixture(t, backend)
			if _, err := s.Create(ctx, CreateRequest{Path: "beads/memory", Body: "not listed"}); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"beads/a", "beads/b"} {
				if _, err := s.CreateIssue(ctx, path, plainIssue(path)); err != nil {
					t.Fatal(err)
				}
			}
			for name, statement := range map[string]string{
				"filtered-missing-memory":   "DELETE FROM graph_preview_payloads WHERE path='beads/memory'",
				"filtered-missing-issue":    "DELETE FROM issues WHERE id=(SELECT backing_key FROM graph_preview_catalog WHERE path='beads/b')",
				"filtered-unknown-backing":  "UPDATE graph_preview_catalog SET backing='other' WHERE path='beads/memory'",
				"filtered-malformed-path":   "UPDATE graph_preview_catalog SET path='beads/../bad' WHERE path='beads/memory'",
				"filtered-kind":             "UPDATE graph_preview_catalog SET resource_kind='link' WHERE path='beads/b'",
				"filtered-wrong-type":       "UPDATE graph_preview_catalog SET type_url='https://wrong.invalid/type' WHERE path='beads/memory'",
				"filtered-invalid-revision": "UPDATE graph_preview_catalog SET revision='bad' WHERE path='beads/b'",
				"filtered-generic-key":      "UPDATE graph_preview_catalog SET backing_key='unexpected' WHERE path='beads/memory'",
				"empty-query-descriptor":    "UPDATE graph_preview_types SET fingerprint='bad' WHERE name='memory'",
			} {
				t.Run(name, func(t *testing.T) {
					// Deliberate corruption in a rollback-only transaction. The base records
					// are API-authored; this is failure injection, never a seeded demo.
					if err := s.withTx(ctx, false, func(tx *sql.Tx) error {
						if _, err := tx.ExecContext(ctx, statement); err != nil {
							return err
						}
						got, err := s.listIssuesInTx(ctx, tx, publicops.ListRequest{TitleContains: "no matches"})
						if !errors.Is(err, ErrInvalidStore) || !reflect.ValueOf(got).IsZero() {
							return fmt.Errorf("corrupt empty page: %+v %w", got, err)
						}
						return nil
					}); err != nil {
						t.Fatal(err)
					}
				})
			}
			if err := s.withTx(ctx, false, func(tx *sql.Tx) error {
				// Sort puts a first and b in the limit=1 probe position. The latter's
				// retained corruption must refuse rather than return a plus HasMore.
				if _, err := tx.ExecContext(ctx, "UPDATE graph_preview_issue_versions SET owned='{}' WHERE path='beads/b'"); err != nil {
					return err
				}
				got, err := s.listIssuesInTx(ctx, tx, publicops.ListRequest{SortBy: "title", Limit: issueListInt(1)})
				if !errors.Is(err, ErrInvalidStore) || !reflect.ValueOf(got).IsZero() {
					return fmt.Errorf("corrupt probe: %+v %w", got, err)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			before := reopenState(t, ctx, s)
			prior := s.options
			s.options.Binding.AuthorityID = "ffffffffffffffffffffffffffffffff"
			got, err := s.ListIssues(ctx, publicops.ListRequest{})
			s.options = prior
			if !errors.Is(err, ErrInvalidStore) || !reflect.ValueOf(got).IsZero() {
				t.Fatalf("authority: %+v %v", got, err)
			}
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			got, err = s.ListIssues(canceled, publicops.ListRequest{})
			if !errors.Is(err, context.Canceled) || !reflect.ValueOf(got).IsZero() {
				t.Fatalf("canceled: %+v %v", got, err)
			}
			for _, request := range []publicops.ListRequest{{Status: "unknown"}, {IssueType: "unknown"}} {
				got, err := s.ListIssues(ctx, request)
				if !errors.Is(err, storage.ErrValidation) || !reflect.ValueOf(got).IsZero() {
					t.Fatalf("filter validation: %+v %v", got, err)
				}
			}
			if !reflect.DeepEqual(before, reopenState(t, ctx, s)) {
				t.Fatal("refusal changed state")
			}
		})
	}
}

func TestGraphIssueListBounds(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s := issueListFixture(t, backend)
			if _, err := s.CreateIssue(ctx, "beads/work", plainIssue("Work")); err != nil {
				t.Fatal(err)
			}
			assertBound := func(got IssueListPage, err error) error {
				if !errors.Is(err, ErrLimitExceeded) || !reflect.ValueOf(got).IsZero() {
					return fmt.Errorf("bound leaked page: %+v %w", got, err)
				}
				return nil
			}
			if err := s.withTx(ctx, false, func(tx *sql.Tx) error {
				// Each value fits the existing TEXT column. Their aggregate,
				// including key bytes, exceeds the adapter's 64KiB admission.
				for _, key := range []string{"types.infra", "types.custom"} {
					if err := issueops.SetConfigInTx(ctx, tx, key, strings.Repeat("x", PreviewIssueListConfigByteLimit/2)); err != nil {
						return err
					}
				}
				got, err := s.listIssuesInTx(ctx, tx, publicops.ListRequest{})
				return assertBound(got, err)
			}); err != nil {
				t.Fatal(err)
			}
			if err := s.withTx(ctx, false, func(tx *sql.Tx) error {
				names := make([]string, PreviewIssueListConfigRowLimit+1)
				for i := range names {
					names[i] = fmt.Sprintf("extra%d", i)
				}
				value := strings.Join(names, ",")
				if _, err := issueops.SyncConfigTables(ctx, tx, "types.custom", value); err != nil {
					return err
				}
				got, err := s.listIssuesInTx(ctx, tx, publicops.ListRequest{})
				return assertBound(got, err)
			}); err != nil {
				t.Fatal(err)
			}
			if err := s.withTx(ctx, false, func(tx *sql.Tx) error {
				// Synthetic catalog excess verifies the operational cap before hydration.
				// This rolled-back bound injection is not application acceptance evidence.
				for i := 0; i < PreviewSnapshotLimit; i++ {
					if _, err := tx.ExecContext(ctx, "INSERT INTO graph_preview_catalog(path,resource_kind,type_url,revision,allocation_state,backing) VALUES (?,'bead',?,'00000000000000000000000000000000','live','generic')", fmt.Sprintf("beads/limit%d", i), MemoryTypeURL(s.ScopeURL())); err != nil {
						return err
					}
				}
				got, err := s.listIssuesInTx(ctx, tx, publicops.ListRequest{Limit: issueListInt(1)})
				return assertBound(got, err)
			}); err != nil {
				t.Fatal(err)
			}
			// Unlike the synthetic count probe, this oversized unrelated Memory is
			// authored through the actual writer and exercises the persisted byte cap.
			if _, err := s.Create(ctx, CreateRequest{Path: "beads/large", Body: strings.Repeat("x", PreviewCurrentReadByteLimit/2)}); err != nil {
				t.Fatal(err)
			}
			got, err := s.ListIssues(ctx, publicops.ListRequest{Limit: issueListInt(1)})
			if err := assertBound(got, err); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGraphIssueListTransactionConsistency(t *testing.T) {
	// Production embedded has one session and is covered by sequential lifecycle
	// reads. Force same-database read/write overlap only on ordinary server.
	ctx, options, reader := issueListFixture(t, "server")
	_, err := reader.CreateIssue(ctx, "beads/work", plainIssue("Work"))
	if err != nil {
		t.Fatal(err)
	}
	writer, err := OpenExisting(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := writer.Close(); err != nil {
			t.Error(err)
		}
	}()
	request := publicops.ListRequest{Status: "open"}
	before, err := reader.ListIssues(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	var closed IssueMutationResult
	if err := reader.withTx(ctx, false, func(tx *sql.Tx) error {
		// Establish the same real read snapshot used by ListIssues before a
		// controlled independent writer commits the close and a policy change.
		if err := checkBinding(ctx, tx, options); err != nil {
			return err
		}
		var err error
		closed, err = writer.CloseIssue(ctx, "beads/work", "overlapping close", "writer")
		if err != nil {
			return err
		}
		if err := writer.withTx(ctx, true, func(writeTx *sql.Tx) error { return issueops.SetConfigInTx(ctx, writeTx, "types.infra", "task") }); err != nil {
			return err
		}
		during, err := reader.listIssuesInTx(ctx, tx, request)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(during, before) {
			return fmt.Errorf("mixed query/config/record snapshot: %+v", during)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	after, err := reader.ListIssues(ctx, request)
	if err != nil || len(after.Items) != 0 {
		t.Fatalf("fresh snapshot: %+v %v", after, err)
	}
	assertIssueListRetained(t, ctx, reader, "beads/work", before.Items[0])
	current, err := reader.ShowIssue(ctx, "beads/work")
	if err != nil || !reflect.DeepEqual(current, closed.Issue) {
		t.Fatalf("writer state: %+v %v", current, err)
	}
}

// Custom classifications preserve mixed-case and non-ASCII text. With only two
// Issues, a limit-one query's extra probe fetches the whole set and can conceal
// a SQL-collation/Go-sort mismatch. Three real authored Issues expose that seam.
// This test specifies page-prefix consistency, not a new shared sort contract.
func TestGraphIssueListCustomTypeCollation(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s := issueListFixture(t, backend)
			setIssueListConfig(t, ctx, s, "types.custom", "Zulu,alpha,éclair")
			created := map[types.IssueType]IssueRecord{}
			for i, classification := range []types.IssueType{"Zulu", "alpha", "éclair"} {
				request := plainIssue("Custom type " + string(classification))
				request.Issue.IssueType = classification
				got, err := s.CreateIssue(ctx, fmt.Sprintf("beads/collation-%d", i), request)
				if err != nil {
					t.Fatalf("author custom type %q: %v", classification, err)
				}
				if got.Properties.IssueType != classification {
					t.Fatalf("custom type spelling changed: got %q want %q", got.Properties.IssueType, classification)
				}
				created[classification] = got
			}
			var collation string
			if err := s.withTx(ctx, false, func(tx *sql.Tx) error {
				return tx.QueryRowContext(ctx, `SELECT COLLATION_NAME FROM information_schema.COLUMNS
                    WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='issues' AND COLUMN_NAME='issue_type'`).Scan(&collation)
			}); err != nil {
				t.Fatalf("read actual Issue type collation: %v", err)
			}
			t.Logf("Issue type column collation: %s", collation)
			before := reopenState(t, ctx, s)
			for _, direction := range []struct {
				name    string
				reverse bool
				types   []types.IssueType
			}{
				{"ascending", false, []types.IssueType{"Zulu", "alpha", "éclair"}},
				{"descending", true, []types.IssueType{"éclair", "alpha", "Zulu"}},
			} {
				t.Run(direction.name, func(t *testing.T) {
					full, err := s.ListIssues(ctx, publicops.ListRequest{SortBy: "type", Reverse: direction.reverse, Limit: issueListInt(0)})
					if err != nil || full.HasMore || len(full.Items) != len(created) {
						t.Fatalf("full custom-type page: %+v %v", full, err)
					}
					for i, classification := range direction.types {
						if !reflect.DeepEqual(full.Items[i], created[classification]) {
							t.Fatalf("unlimited shared type sort changed complete record at %d: got=%+v want=%+v", i, full.Items[i], created[classification])
						}
					}
					for limit := 1; limit <= len(full.Items); limit++ {
						t.Run(fmt.Sprintf("limit-%d", limit), func(t *testing.T) {
							page, err := s.ListIssues(ctx, publicops.ListRequest{SortBy: "type", Reverse: direction.reverse, Limit: issueListInt(limit)})
							if err != nil || page.HasMore != (limit < len(full.Items)) || !reflect.DeepEqual(page.Items, full.Items[:limit]) {
								t.Fatalf("limited type page is not full-result prefix (collation=%s, reverse=%t, limit=%d): got=%+v want=%+v error=%v", collation, direction.reverse, limit, page, full.Items[:limit], err)
							}
						})
					}
				})
			}
			if !reflect.DeepEqual(before, reopenState(t, ctx, s)) {
				t.Fatal("custom-type sort or pagination mutated state")
			}
		})
	}
}

// Deletion keeps a reserved allocation and its final live head. Query admission
// must validate that state without putting the removed Memory back in a view.
func TestGraphIssueQueriesDeletedMemory(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s, source, target, _ := reopenFixture(t, backend)
			memory, err := s.Create(ctx, CreateRequest{Path: "beads/removed", Body: "retained context"})
			if err != nil {
				t.Fatal(err)
			}
			wantList, err := s.ListIssues(ctx, publicops.ListRequest{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.DeleteMemory(ctx, MemoryDeleteRequest{Path: "beads/removed", ExpectedRevision: memory.Revision}); err != nil {
				t.Fatal(err)
			}
			before := reopenState(t, ctx, s)
			got, err := s.ListIssues(ctx, publicops.ListRequest{})
			if err != nil || !reflect.DeepEqual(got, wantList) {
				t.Fatalf("deleted Memory changed Issue list: %+v %v", got, err)
			}
			empty, err := s.ListIssues(ctx, publicops.ListRequest{TitleContains: "no such title"})
			if err != nil || empty.Items == nil || len(empty.Items) != 0 || empty.HasMore {
				t.Fatalf("empty query rejected valid deletion: %+v %v", empty, err)
			}
			assertGraphBlocked(t, ctx, s, source, target)
			snapshot, err := s.CurrentSnapshot(ctx)
			if err != nil || len(snapshot.Records) != 3 {
				t.Fatalf("current snapshot after deletion: %+v %v", snapshot, err)
			}
			for _, record := range snapshot.Records {
				if item, ok := record.(Record); ok && item.ID == memory.ID {
					t.Fatal("deleted Memory emitted")
				}
			}
			retained, err := s.ReadVersion(ctx, "beads/removed", memory.Version)
			if err != nil || !sameBlockedJSON(t, retained, memory) {
				t.Fatalf("final live head changed: %+v %v", retained, err)
			}
			// Rollback-only failure injection into an API-authored deleted allocation;
			// it is never installed fixture data and cannot survive this transaction.
			rollback := errors.New("rollback deleted-head corruption")
			err = s.withTx(ctx, false, func(tx *sql.Tx) error {
				if _, err := tx.ExecContext(ctx, "UPDATE graph_preview_versions SET snapshot='{}' WHERE path=? AND version=?", "beads/removed", memory.Version); err != nil {
					return err
				}
				listed, listErr := s.listIssuesInTx(ctx, tx, publicops.ListRequest{TitleContains: "no such title"})
				blocked, blockedErr := s.blockedIssuesInTx(ctx, tx)
				if !errors.Is(listErr, ErrInvalidStore) || !reflect.DeepEqual(listed, IssueListPage{}) || !errors.Is(blockedErr, ErrInvalidStore) || blocked != nil {
					t.Errorf("corrupt deleted head yielded a view: list=%+v/%v blocked=%+v/%v", listed, listErr, blocked, blockedErr)
				}
				return rollback
			})
			if !errors.Is(err, rollback) {
				t.Fatalf("corruption control failed: %v", err)
			}
			if !reflect.DeepEqual(before, reopenState(t, ctx, s)) {
				t.Fatal("query changed current, reserved or retained state")
			}
		})
	}
}

func TestGraphIssueQueriesDeletedMemoryReadBudget(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s := issueListFixture(t, backend)
			memory, err := s.Create(ctx, CreateRequest{Path: "beads/deleted", Body: strings.Repeat("d", 3<<20)})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.DeleteMemory(ctx, MemoryDeleteRequest{Path: "beads/deleted", ExpectedRevision: memory.Revision}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ListIssues(ctx, publicops.ListRequest{}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.BlockedIssues(ctx); err != nil {
				t.Fatal(err)
			}
			// Two live copies fit alone; the separately retained deleted head causes
			// the existing 16 MiB acquisition budget to refuse even these empty views.
			if _, err := s.Create(ctx, CreateRequest{Path: "beads/filler", Body: strings.Repeat("f", 7<<20)}); err != nil {
				t.Fatal(err)
			}
			before := reopenState(t, ctx, s)
			listed, listErr := s.ListIssues(ctx, publicops.ListRequest{Limit: issueListInt(1)})
			blocked, blockedErr := s.BlockedIssues(ctx)
			if !errors.Is(listErr, ErrLimitExceeded) || !reflect.DeepEqual(listed, IssueListPage{}) || !errors.Is(blockedErr, ErrLimitExceeded) || blocked != nil {
				t.Fatalf("deleted head bypassed budget: list=%+v/%v blocked=%+v/%v", listed, listErr, blocked, blockedErr)
			}
			if !reflect.DeepEqual(before, reopenState(t, ctx, s)) {
				t.Fatal("budget refusal changed state")
			}
		})
	}
}
