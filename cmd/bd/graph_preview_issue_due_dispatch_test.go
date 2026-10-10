//go:build cgo

package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/graphstore"
	"github.com/steveyegge/beads/internal/types"
)

func TestGraphPreviewIssueDueCreateInput(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"empty", "", ""},
		{"offset", "2030-01-02T03:04:05-07:00", "2030-01-02T10:04:05Z"},
		{"fraction-for-storage", "2030-01-02T03:04:05.6Z", "2030-01-02T03:04:05.6Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := issueCreateFieldsCommand(t)
			cmd.Flags().String("due", "", "")
			if err := cmd.ParseFlags([]string{"--due=" + tc.input}); err != nil {
				t.Fatal(err)
			}
			issue := types.Issue{}
			if err := graphPreviewIssueCreateFields(cmd, &issue); err != nil {
				t.Fatal(err)
			}
			if tc.want == "" {
				if issue.DueAt != nil {
					t.Fatal("empty create became present")
				}
				return
			}
			if issue.DueAt == nil || issue.DueAt.Format(time.RFC3339Nano) != tc.want {
				t.Fatalf("due changed before storage: %v", issue.DueAt)
			}
		})
	}
}

// The real command dispatcher must select Issue writes for due-only updates and
// pass date filters to the normal checked list path; no test-only store setup.
func TestGraphPreviewIssueDueDispatch(t *testing.T) {
	bd := buildBDUnderTest(t)
	work, home := t.TempDir(), t.TempDir()
	graphPolicyCLI(t, bd, work, home, nil, "", "init", "--graph-mode", "link", "--prefix", "due", "--scope-url", "https://example.invalid/due/", "--skip-hooks", "--skip-agents", "--non-interactive", "--json")
	created := graphPolicyCLI(t, bd, work, home, nil, "", "create", "Due work", "--id", "beads/work", "--due", "2030-01-02T03:04:05-07:00", "--json")
	var first struct{ Result graphstore.IssueRecord }
	if err := json.Unmarshal([]byte(created), &first); err != nil {
		t.Fatal(err)
	}
	if first.Result.Properties.DueAt == nil || first.Result.Properties.DueAt.Format(time.RFC3339) != "2030-01-02T10:04:05Z" {
		t.Fatal("create dispatcher dropped due instant")
	}
	updated := graphPolicyCLI(t, bd, work, home, nil, "", "update", "beads/work", "--due=", "--if-revision", first.Result.Revision, "--json")
	var changed struct {
		Result graphstore.IssueMutationResult
	}
	if err := json.Unmarshal([]byte(updated), &changed); err != nil {
		t.Fatal(err)
	}
	if !changed.Result.Changed || changed.Result.Issue.Properties.DueAt != nil || changed.Result.Issue.Revision == first.Result.Revision {
		t.Fatal("due clear did not reach Issue writer")
	}
	if exact := graphPolicyCLI(t, bd, work, home, nil, "", "show", "beads/work", "--version", first.Result.Revision, "--json"); exact != created {
		t.Fatal("clearing due changed initial retained record")
	}
	graphPolicyCLI(t, bd, work, home, nil, "", "update", "beads/work", "--due=2000-01-01T00:00:00Z", "--if-revision", changed.Result.Issue.Revision, "--json")
	listed := graphPolicyCLI(t, bd, work, home, nil, "", "list", "--format", "records-json", "--due-after=1999-01-01T00:00:00Z", "--due-before=2001-01-01T00:00:00Z", "--overdue", "--all", "--limit=2")
	var page struct{ Result graphstore.IssueListPage }
	if err := json.Unmarshal([]byte(listed), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Result.Items) != 1 || page.Result.Items[0].ID != first.Result.ID || page.Result.HasMore {
		t.Fatal("due filters did not select complete Issue")
	}
	before := graphPolicyCLI(t, bd, work, home, nil, "", "show", "beads/work", "--json")
	for _, tc := range []struct {
		name, code string
		args       []string
	}{
		{"invalid-create", "invalid_properties", []string{"create", "Bad", "--id", "beads/bad", "--due=not-a-date"}},
		{"invalid-update", "invalid_properties", []string{"update", "beads/work", "--due=not-a-date"}},
		{"defer-held", "capability_unavailable", []string{"update", "beads/work", "--due=", "--defer="}},
		{"create-status-held", "capability_unavailable", []string{"create", "Bad", "--id", "beads/bad", "--due=2030-01-01", "--status=open"}},
		{"readonly-before-parse", "permission_denied", []string{"update", "beads/work", "--due=not-a-date", "--readonly"}},
	} {
		t.Run(tc.name, func(t *testing.T) { graphPolicyCLI(t, bd, work, home, nil, tc.code, append(tc.args, "--json")...) })
	}
	if current := graphPolicyCLI(t, bd, work, home, nil, "", "show", "beads/work", "--json"); current != before {
		t.Fatal("refusal changed current Issue")
	}
	graphPolicyCLI(t, bd, work, home, nil, "not_found", "show", "beads/bad", "--json")
	status := graphPolicyCLI(t, bd, work, home, nil, "", "status", "--graph", "--json")
	var capabilities struct {
		Result struct{ Capabilities map[string]bool }
	}
	if err := json.Unmarshal([]byte(status), &capabilities); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{"issueDueDate": true, "issueDueFilter": true, "issueWorkflows": false} {
		got, present := capabilities.Result.Capabilities[name]
		if !present || got != want {
			t.Fatalf("capability %s = %v present=%v, want %v", name, got, present, want)
		}
	}
}
