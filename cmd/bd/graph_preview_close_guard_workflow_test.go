//go:build cgo

package main

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/graphstore"
	"github.com/steveyegge/beads/internal/types"
)

// A graph Issue's opaque revision is a complete-record guard, including its
// owned Link projection. Guarded close uses the native single-Issue writer and
// refuses stale retries before the already-closed no-op can mask a conflict.
func TestGraphPreviewCloseGuardWorkflow(t *testing.T) {
	bd := buildBDUnderTest(t)
	for _, engine := range []string{"embedded", "server"} {
		t.Run(engine, func(t *testing.T) {
			work, home := t.TempDir(), t.TempDir()
			const scope = "https://example.invalid/close-guard/"
			initArgs := []string{"init", "--graph-mode", "link", "--scope-url", scope,
				"--skip-hooks", "--skip-agents", "--non-interactive"}
			if engine == "server" {
				port := os.Getenv("BEADS_GRAPH_TEST_SERVER_PORT")
				if port == "" {
					t.Skip("set BEADS_GRAPH_TEST_SERVER_PORT for ordinary shared-server qualification")
				}
				initArgs = append(initArgs, "--server", "--external", "--server-host", "127.0.0.1",
					"--server-port", port, "--server-user", "root")
			}
			call := func(args ...string) string {
				t.Helper()
				return graphPolicyCLI(t, bd, work, home, nil, "", append(args, "--json")...)
			}
			refuse := func(wantCode int, want string, args ...string) {
				t.Helper()
				stdout, stderr, code := graphVersionsProcess(t, bd, work, home, append(args, "--json")...)
				if code != wantCode || stdout != "" || !strings.Contains(stderr, want) {
					t.Fatalf("bd %v: exit=%d stdout=%q stderr=%q; want exit=%d and %q", args, code, stdout, stderr, wantCode, want)
				}
			}
			call(initArgs...)
			call("create", "First", "--id", "first")
			call("create", "Second", "--id", "second")
			before := graphMixedResult[graphstore.IssueRecord](t, call("show", "first"))
			versionsBefore := call("versions", "first")
			refuse(4, "revision_conflict", "close", "first", "--if-revision", "stale", "--reason", "done")
			refuse(2, "invalid_selector", "close", "first", "second", "--if-revision", before.Revision)
			refuse(2, "invalid_selector", "close", "first", "--if-revision", before.Revision, "--claim-next")
			refuse(2, "invalid_selector", "close", "first", "--if-revision", before.Revision, "--suggest-next")
			if got := call("versions", "first"); got != versionsBefore {
				t.Fatal("a refused guard or combination changed the Issue")
			}
			closed := graphMixedResult[graphstore.IssueMutationResult](t,
				call("close", "first", "--if-revision", before.Revision, "--reason", "done"))
			if !closed.Changed || closed.Issue.Properties.Status != types.StatusClosed ||
				closed.Issue.Revision == before.Revision || closed.Issue.Properties.CloseReason != "done" {
				t.Fatalf("guarded close did not use the native Issue lifecycle: %+v", closed)
			}
			versionsClosed := call("versions", "first")
			if versionsClosed == versionsBefore {
				t.Fatal("guarded close did not retain a successor")
			}
			refuse(4, "revision_conflict", "close", "first", "--if-revision", before.Revision, "--reason", "retry")
			retry := graphMixedResult[graphstore.IssueMutationResult](t,
				call("close", "first", "--if-revision", closed.Issue.Revision, "--reason", "retry"))
			if retry.Changed || !reflect.DeepEqual(retry.Issue, closed.Issue) || call("versions", "first") != versionsClosed {
				t.Fatalf("accepted already-closed retry minted another version or changed the first reason: %+v", retry)
			}
		})
	}
}
