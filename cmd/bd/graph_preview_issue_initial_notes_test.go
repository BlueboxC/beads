//go:build cgo

package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/graphstore"
	"github.com/steveyegge/beads/internal/types"
)

func TestGraphPreviewIssueInitialNotesInput(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		supplied    bool
		invalid     bool
	}{
		{name: "omitted"},
		{name: "empty", supplied: true},
		{name: "literal", value: "  雪\r\ncafé e\u0301\n  ", supplied: true},
		{name: "hyphen", value: "-", supplied: true},
		{name: "invalid-utf8", value: "\xff", supplied: true, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := issueCreateFieldsCommand(t)
			if tc.supplied {
				if err := cmd.ParseFlags([]string{"--notes=" + tc.value}); err != nil {
					t.Fatal(err)
				}
			}
			i := types.Issue{Title: "Preserve", Status: types.StatusOpen, Owner: "supplied-owner", CreatedBy: "supplied-creator"}
			var err error
			diagnostic := captureStderr(t, func() { err = graphPreviewIssueCreateFields(cmd, &i) })
			if tc.invalid {
				var failure *exitError
				if !errors.As(err, &failure) || failure.Code != 2 || !strings.Contains(diagnostic, "invalid_properties") {
					t.Fatalf("invalid notes not refused before storage: %v %q", err, diagnostic)
				}
				return
			}
			if err != nil || i.Notes != tc.value || i.Title != "Preserve" || i.Status != types.StatusOpen || i.Owner != "supplied-owner" || i.CreatedBy != "supplied-creator" {
				t.Fatalf("literal input/default boundary changed: %+v %v", i, err)
			}
		})
	}
}

// Each command below is a new installed process. The same workflow runs on
// embedded Dolt and an ordinary shared-server database when the test server
// port is supplied, so the CLI gate and native Issue writer are both exercised.
func TestGraphPreviewIssueNotesReplaceAndClearWorkflow(t *testing.T) {
	bd := buildBDUnderTest(t)
	for _, engine := range []string{"embedded", "server"} {
		t.Run(engine, func(t *testing.T) {
			work, home := t.TempDir(), t.TempDir()
			args := []string{"init", "--graph-mode", "link", "--scope-url", "https://example.invalid/issue-notes/", "--skip-hooks", "--skip-agents", "--non-interactive"}
			if engine == "server" {
				port := os.Getenv("BEADS_GRAPH_TEST_SERVER_PORT")
				if port == "" {
					t.Skip("set BEADS_GRAPH_TEST_SERVER_PORT for ordinary shared-server notes qualification")
				}
				args = append(args, "--server", "--external", "--server-host", "127.0.0.1", "--server-port", port, "--server-user", "root")
			}
			call := func(args ...string) string {
				t.Helper()
				return graphPolicyCLI(t, bd, work, home, nil, "", append(args, "--json")...)
			}
			refuse := func(code string, args ...string) {
				t.Helper()
				graphPolicyCLI(t, bd, work, home, nil, code, append(args, "--json")...)
			}
			call(args...)
			var status struct {
				Result struct {
					Capabilities map[string]bool `json:"capabilities"`
				} `json:"result"`
			}
			if err := json.Unmarshal([]byte(call("status", "--graph")), &status); err != nil {
				t.Fatal(err)
			}
			if !status.Result.Capabilities["issueNotesReplace"] || !status.Result.Capabilities["issueNotesClear"] {
				t.Fatal("graph status omitted supported Issue notes capabilities")
			}
			const path = "beads/work"
			created := graphMixedResult[graphstore.IssueRecord](t, call("create", "Notes work", "--id", path, "--notes=First", "--design=Keep", "--actor=author"))
			versions := call("versions", path)
			refuse("notes_overwrite_refused", "update", path, "--notes=Second", "--if-revision", created.Revision)
			refuse("invalid_properties", "update", path, "--notes=", "--force", "--if-revision", created.Revision)
			if got := graphMixedResult[graphstore.IssueRecord](t, call("show", path)); !reflect.DeepEqual(got, created) || call("versions", path) != versions {
				t.Fatal("refused notes edit changed current Issue or retained history")
			}
			noop := graphMixedResult[graphstore.IssueMutationResult](t, call("update", path, "--notes=First", "--if-revision", created.Revision))
			if noop.Changed || !reflect.DeepEqual(noop.Issue, created) || call("versions", path) != versions {
				t.Fatal("identical notes replacement minted a version")
			}
			replaced := graphMixedResult[graphstore.IssueMutationResult](t, call("update", path, "--notes=Second", "--force", "--title=Revised", "--if-revision", created.Revision, "--actor=editor"))
			if !replaced.Changed || replaced.Issue.Revision == created.Revision || replaced.Issue.Properties.Notes != "Second" || replaced.Issue.Properties.Title != "Revised" || replaced.Issue.Properties.Design != "Keep" || replaced.Issue.Attribution.Actor != "editor" {
				t.Fatalf("forced notes update lost native fields or attribution: %+v", replaced)
			}
			refuse("revision_conflict", "update", path, "--notes=Stale", "--force", "--if-revision", created.Revision)
			prior := graphMixedResult[graphstore.IssueRecord](t, call("show", path, "--version", created.Revision))
			if prior.Properties.Notes != "First" || prior.Properties.Title != "Notes work" {
				t.Fatal("replacement rewrote the retained initial Issue")
			}
			appended := graphMixedResult[graphstore.IssueMutationResult](t, call("update", path, "--append-notes=Third", "--if-revision", replaced.Issue.Revision))
			if !appended.Changed || appended.Issue.Properties.Notes != "Second\nThird" {
				t.Fatal("append after replacement did not preserve both notes")
			}
			cleared := graphMixedResult[graphstore.IssueMutationResult](t, call("update", path, "--clear-notes", "--if-revision", appended.Issue.Revision))
			if !cleared.Changed || cleared.Issue.Properties.Notes != "" || cleared.Issue.Properties.Title != "Revised" {
				t.Fatal("explicit clear changed another field or retained notes")
			}
			if got := graphMixedResult[graphstore.IssueRecord](t, call("show", path, "--version", appended.Issue.Revision)); got.Properties.Notes != "Second\nThird" {
				t.Fatal("clear rewrote prior retained notes")
			}
			versions = call("versions", path)
			noop = graphMixedResult[graphstore.IssueMutationResult](t, call("update", path, "--clear-notes", "--if-revision", cleared.Issue.Revision))
			if noop.Changed || call("versions", path) != versions {
				t.Fatal("clearing empty notes minted a version")
			}
			fromEmpty := graphMixedResult[graphstore.IssueMutationResult](t, call("update", path, "--notes=Fresh"))
			if !fromEmpty.Changed || fromEmpty.Issue.Properties.Notes != "Fresh" {
				t.Fatal("setting notes on an empty Issue required force")
			}
		})
	}
}

func TestGraphPreviewIssueCreateAuthorshipDispatch(t *testing.T) {
	bd := buildBDUnderTest(t)
	work, home := t.TempDir(), t.TempDir()
	gitconfig := filepath.Join(home, "missing-gitconfig") // graphPolicyCLI's isolated global config.
	graphPolicyCLI(t, bd, work, home, nil, "", "init", "--graph-mode", "link", "--prefix", "auth", "--scope-url", "https://example.invalid/initial-notes/", "--skip-hooks", "--skip-agents", "--non-interactive", "--json")
	for _, tc := range []struct {
		name, flagActor, beadsActor, oldActor, ownerEnv, gitEmail, wantActor, wantOwner string
	}{
		{"explicit", "explicit-creator", "beads-creator", "old-creator", "  env-owner@example.invalid  ", "git-owner@example.invalid", "explicit-creator", "  env-owner@example.invalid  "},
		{"primary-env", "", "beads-creator", "old-creator", "", "git-owner@example.invalid", "beads-creator", "git-owner@example.invalid"},
		{"deprecated-env", "", "", "old-creator", "", "git-owner@example.invalid", "old-creator", "git-owner@example.invalid"},
		{"git-fallback", "", "", "", "", "git-owner@example.invalid", "Git Creator", "git-owner@example.invalid"},
		{"missing-owner", "", "", "", "", "", "Git Creator", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gitText := "[user]\n\tname = Git Creator\n"
			if tc.gitEmail != "" {
				gitText += "\temail = " + tc.gitEmail + "\n"
			}
			writeFile(t, gitconfig, []byte(gitText))
			env := []string{"BEADS_ACTOR=" + tc.beadsActor, "BD_ACTOR=" + tc.oldActor, "GIT_AUTHOR_EMAIL=" + tc.ownerEnv}
			path := "beads/" + tc.name
			args := []string{"create", "Authored Issue", "--id", path, "--notes", "  Initial 雪\r\n ", "--assignee", "assigned-worker", "--json"}
			if tc.flagActor != "" {
				args = append(args, "--actor", tc.flagActor)
			}
			created := graphPolicyCLI(t, bd, work, home, env, "", args...)
			var envelope struct {
				Result struct {
					Revision    string
					Properties  types.Issue
					Attribution struct{ Actor string }
				}
			}
			if err := json.Unmarshal([]byte(created), &envelope); err != nil {
				t.Fatal(err)
			}
			i := envelope.Result.Properties
			if envelope.Result.Revision == "" || i.CreatedBy != tc.wantActor || i.Owner != tc.wantOwner || envelope.Result.Attribution.Actor != tc.wantActor || i.Assignee != "assigned-worker" || i.Status != types.StatusOpen || i.Notes != "  Initial 雪\r\n " || i.LeaseExpiresAt != nil || i.HeartbeatAt != nil {
				t.Fatalf("authorship, initial notes or no-claim boundary lost: %+v", envelope.Result)
			}
			for _, flags := range [][]string{nil, {"--version", envelope.Result.Revision}} {
				if got := graphPolicyCLI(t, bd, work, home, nil, "", append([]string{"show", path, "--json"}, flags...)...); got != created {
					t.Fatal("fresh current/exact read changed complete initial authored Issue")
				}
			}
			graphPolicyCLI(t, bd, work, home, nil, "invalid_properties", "update", path, "--notes=", "--if-revision", envelope.Result.Revision, "--json")
			if got := graphPolicyCLI(t, bd, work, home, nil, "", "show", path, "--json"); got != created {
				t.Fatal("held notes clear changed initial Issue")
			}
		})
	}
	for _, key := range []string{"BEADS_ACTOR", "GIT_AUTHOR_EMAIL"} {
		t.Run("overlong-"+key, func(t *testing.T) {
			env := []string{key + "=" + strings.Repeat("雪", 256)}
			graphPolicyCLI(t, bd, work, home, env, "invalid_properties", "create", "Refused", "--id", "beads/refused", "--notes=No allocation", "--json")
			graphPolicyCLI(t, bd, work, home, nil, "not_found", "show", "beads/refused", "--json")
		})
	}
	graphPolicyCLI(t, bd, work, home, nil, "permission_denied", "create", "Read-only", "--id", "beads/refused", "--notes", "must not allocate", "--readonly", "--json")
	graphPolicyCLI(t, bd, work, home, nil, "not_found", "show", "beads/refused", "--json")
}
