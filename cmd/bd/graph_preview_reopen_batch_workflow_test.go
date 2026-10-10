//go:build cgo

package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/graphstore"
	"github.com/steveyegge/beads/internal/types"
)

// Each call is a separate installed process against a fresh graph workspace.
// A shared Dolt server is supplied by the qualification runner.
func TestGraphPreviewCloseReopenBatchWorkflow(t *testing.T) {
	bd := buildBDUnderTest(t)
	for _, engine := range []string{"embedded", "server"} {
		t.Run(engine, func(t *testing.T) {
			work, home := t.TempDir(), t.TempDir()
			args := []string{"init", "--graph-mode", "link", "--scope-url", "https://example.invalid/reopen-batch/", "--skip-hooks", "--skip-agents", "--non-interactive"}
			if engine == "server" {
				port := os.Getenv("BEADS_GRAPH_TEST_SERVER_PORT")
				if port == "" {
					t.Skip("set BEADS_GRAPH_TEST_SERVER_PORT for ordinary shared-server CLI qualification")
				}
				args = append(args, "--server", "--external", "--server-host", "127.0.0.1", "--server-port", port, "--server-user", "root")
			}
			call := func(args ...string) string {
				t.Helper()
				return graphPolicyCLI(t, bd, work, home, nil, "", append(args, "--json")...)
			}
			call(args...)
			call("create", "First", "--id", "beads/first")
			call("create", "Second", "--id", "beads/second")
			closed := graphMixedResult[[]graphstore.IssueMutationResult](t, call("close", "first", "second", "--reason", "First done", "--reason", "Second done"))
			if len(closed) != 2 || !closed[0].Changed || !closed[1].Changed ||
				closed[0].Issue.Properties.Status != types.StatusClosed || closed[1].Issue.Properties.Status != types.StatusClosed {
				t.Fatalf("batch did not close both Issues in input order: %+v", closed)
			}
			firstBefore, secondBefore := call("versions", "first"), call("versions", "second")

			reopened := graphMixedResult[[]graphstore.IssueMutationResult](t, call("reopen", "first", "second", "--reason", "More work"))
			if len(reopened) != 2 || !reopened[0].Changed || !reopened[1].Changed ||
				reopened[0].Issue.Properties.Status != types.StatusOpen || reopened[1].Issue.Properties.Status != types.StatusOpen {
				t.Fatalf("batch did not reopen both Issues in input order: %+v", reopened)
			}
			if call("versions", "first") == firstBefore || call("versions", "second") == secondBefore {
				t.Fatal("batch reopen did not retain both Issue successors")
			}

			runPartial := func(args ...string) string {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
				defer cancel()
				partial := exec.CommandContext(ctx, bd, append(args, "--json")...)
				partial.Dir = work
				partial.Env = []string{
					"PATH=" + os.Getenv("PATH"), "HOME=" + home,
					"TMPDIR=" + os.TempDir(), "TMP=" + os.TempDir(), "TEMP=" + os.TempDir(),
					"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
					"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
					"GIT_CONFIG_GLOBAL=" + filepath.Join(home, "missing-gitconfig"), "GIT_CONFIG_NOSYSTEM=1",
					"BD_DISABLE_METRICS=1", "BD_DISABLE_EVENT_FLUSH=1", "BD_NON_INTERACTIVE=1",
					"BEADS_DOLT_AUTO_START=0", "DOLT_METRICS_DISABLED=1", "NO_COLOR=1",
				}
				var stdout, stderr bytes.Buffer
				partial.Stdout, partial.Stderr = &stdout, &stderr
				err := partial.Run()
				if ctx.Err() != nil {
					t.Fatalf("batch hung: %v", ctx.Err())
				}
				if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
					t.Fatalf("partial batch exit = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
				}
				if !strings.Contains(stderr.String(), `"code":"not_found"`) {
					t.Fatalf("missing target was not reported on stderr: %s", stderr.String())
				}
				return stdout.String()
			}

			call("close", "first")
			changed := graphMixedResult[[]graphstore.IssueMutationResult](t, runPartial("reopen", "first", "missing"))
			if len(changed) != 1 || !changed[0].Changed || changed[0].Issue.Properties.Status != types.StatusOpen {
				t.Fatalf("partial batch hid the successful reopen: %+v", changed)
			}
			call("create", "Third", "--id", "beads/third")
			closedPartial := graphMixedResult[[]graphstore.IssueMutationResult](t, runPartial("close", "third", "missing"))
			if len(closedPartial) != 1 || !closedPartial[0].Changed || closedPartial[0].Issue.Properties.Status != types.StatusClosed {
				t.Fatalf("partial batch hid the successful close: %+v", closedPartial)
			}
			call("reopen", "third")
			positional := graphMixedResult[graphstore.IssueMutationResult](t, call("done", "third", "Positional reason"))
			if !positional.Changed || positional.Issue.Properties.CloseReason != "Positional reason" {
				t.Fatalf("done alias lost its positional reason: %+v", positional)
			}
			call("create", "Fourth", "--id", "beads/fourth")
			reasonFile := filepath.Join(work, "close-reason.txt")
			if err := os.WriteFile(reasonFile, []byte("Reason from file\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			filed := graphMixedResult[graphstore.IssueMutationResult](t, call("close", "fourth", "--reason-file", reasonFile))
			if !filed.Changed || filed.Issue.Properties.CloseReason != "Reason from file\n" {
				t.Fatalf("close lost its reason file: %+v", filed)
			}
		})
	}
}

// --force and --session take the native Issue close route on both installed
// engines; retrying a closed Issue cannot replace its first close reason or
// session, and a refused close cannot create a graph version.
func TestGraphPreviewCloseForceAndSessionWorkflow(t *testing.T) {
	bd := buildBDUnderTest(t)
	for _, engine := range []string{"embedded", "server"} {
		t.Run(engine, func(t *testing.T) {
			work, home := t.TempDir(), t.TempDir()
			initArgs := []string{"init", "--graph-mode", "link", "--scope-url", "https://example.invalid/close-policy/", "--skip-hooks", "--skip-agents", "--non-interactive"}
			if engine == "server" {
				port := os.Getenv("BEADS_GRAPH_TEST_SERVER_PORT")
				if port == "" {
					t.Skip("set BEADS_GRAPH_TEST_SERVER_PORT for ordinary shared-server CLI qualification")
				}
				initArgs = append(initArgs, "--server", "--external", "--server-host", "127.0.0.1", "--server-port", port, "--server-user", "root")
			}
			call := func(extraEnv []string, code string, args ...string) string {
				t.Helper()
				return graphPolicyCLI(t, bd, work, home, extraEnv, code, append(args, "--json")...)
			}
			call(nil, "", initArgs...)
			call(nil, "", "create", "Prerequisite", "--id", "beads/prerequisite")
			call(nil, "", "create", "Blocked work", "--id", "beads/work")
			call(nil, "", "dep", "add", "work", "prerequisite")
			before := call(nil, "", "versions", "work")
			call(nil, "constraint_violation", "close", "work", "--reason", "Not yet")
			if got := call(nil, "", "versions", "work"); got != before {
				t.Fatal("refused blocked close recorded a version")
			}
			closed := graphMixedResult[graphstore.IssueMutationResult](t, call(nil, "", "close", "work", "--force", "--reason", "Override", "--session", "session-one"))
			if !closed.Changed || closed.Issue.Properties.Status != types.StatusClosed || closed.Issue.Properties.CloseReason != "Override" || closed.Issue.Properties.ClosedBySession != "session-one" {
				t.Fatalf("forced close lost native state or session: %+v", closed)
			}
			closedVersions := call(nil, "", "versions", "work")
			if closedVersions == before {
				t.Fatal("forced close did not retain its Issue successor")
			}
			repeated := graphMixedResult[graphstore.IssueMutationResult](t, call(nil, "", "close", "work", "--force", "--reason", "Must not replace", "--session", "session-two"))
			if repeated.Changed || repeated.Issue.Properties.CloseReason != "Override" || repeated.Issue.Properties.ClosedBySession != "session-one" || call(nil, "", "versions", "work") != closedVersions {
				t.Fatalf("re-close changed the first close or retained a successor: %+v", repeated)
			}

			call(nil, "", "create", "Assigned work", "--id", "beads/assigned", "--assignee", "foreign-holder")
			assignedBefore := call(nil, "", "versions", "assigned")
			call(nil, "invalid_properties", "close", "assigned")
			if call(nil, "", "versions", "assigned") != assignedBefore {
				t.Fatal("refused foreign-holder close recorded a version")
			}
			forced := graphMixedResult[graphstore.IssueMutationResult](t, call([]string{"CLAUDE_SESSION_ID=session-from-env"}, "", "close", "assigned", "--force"))
			if !forced.Changed || forced.Issue.Properties.ClosedBySession != "session-from-env" || forced.Issue.Properties.Status != types.StatusClosed {
				t.Fatalf("forced holder close lost session fallback: %+v", forced)
			}
		})
	}
}

// A close can hand the user newly unblocked work without claiming or
// versioning that work. The suggestion uses the native dependency query but
// exposes only canonical graph Issues.
func TestGraphPreviewCloseSuggestNextWorkflow(t *testing.T) {
	bd := buildBDUnderTest(t)
	for _, engine := range []string{"embedded", "server"} {
		t.Run(engine, func(t *testing.T) {
			work, home := t.TempDir(), t.TempDir()
			initArgs := []string{"init", "--graph-mode", "link", "--scope-url", "https://example.invalid/close-suggest/", "--skip-hooks", "--skip-agents", "--non-interactive"}
			if engine == "server" {
				port := os.Getenv("BEADS_GRAPH_TEST_SERVER_PORT")
				if port == "" {
					t.Skip("set BEADS_GRAPH_TEST_SERVER_PORT for ordinary shared-server CLI qualification")
				}
				initArgs = append(initArgs, "--server", "--external", "--server-host", "127.0.0.1", "--server-port", port, "--server-user", "root")
			}
			call := func(code string, args ...string) string {
				t.Helper()
				return graphPolicyCLI(t, bd, work, home, nil, code, append(args, "--json")...)
			}
			call("", initArgs...)
			call("", "create", "Prerequisite", "--id", "prerequisite")
			call("", "create", "Dependent", "--id", "dependent")
			call("", "dep", "add", "dependent", "prerequisite")
			dependentBefore := call("", "versions", "dependent")
			prerequisiteBefore := call("", "versions", "prerequisite")
			call("", "create", "Unrelated", "--id", "unrelated")
			unrelatedBefore := call("", "versions", "unrelated")
			call("invalid_selector", "close", "prerequisite", "unrelated", "--suggest-next")
			if call("", "versions", "prerequisite") != prerequisiteBefore || call("", "versions", "unrelated") != unrelatedBefore {
				t.Fatal("multi-target suggestion refusal changed an Issue")
			}
			type suggestion struct {
				Closed    []graphstore.IssueRecord `json:"closed"`
				Unblocked []graphstore.IssueRecord `json:"unblocked"`
			}
			first := graphMixedResult[suggestion](t, call("", "close", "prerequisite", "--suggest-next", "--reason", "Done"))
			if len(first.Closed) != 1 || first.Closed[0].Properties.Status != types.StatusClosed ||
				len(first.Unblocked) != 1 || first.Unblocked[0].ID != "https://example.invalid/close-suggest/beads/dependent" {
				t.Fatalf("close did not name newly unblocked Issue: %+v", first)
			}
			if call("", "versions", "dependent") != dependentBefore {
				t.Fatal("suggestion changed or claimed the dependent Issue")
			}
			closedVersions := call("", "versions", "prerequisite")
			if closedVersions == prerequisiteBefore {
				t.Fatal("close did not retain its Issue successor")
			}
			again := graphMixedResult[suggestion](t, call("", "close", "prerequisite", "--suggest-next", "--reason", "Different"))
			if len(again.Unblocked) != 1 || call("", "versions", "prerequisite") != closedVersions {
				t.Fatalf("idempotent close lost suggestion or minted a version: %+v", again)
			}
			alone := graphMixedResult[graphstore.IssueMutationResult](t, call("", "close", "unrelated", "--suggest-next"))
			if !alone.Changed || alone.Issue.Properties.Status != types.StatusClosed {
				t.Fatalf("empty suggestion did not preserve close result: %+v", alone)
			}
		})
	}
}
