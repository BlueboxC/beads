//go:build cgo

package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/graphstore"
	"github.com/steveyegge/beads/internal/types"
)

// An installed graph workspace must close and claim in one durable act. A
// refused sibling does not discard either landing, while retrying a no-op
// close never claims a second Issue or creates another retained version.
func TestGraphPreviewCloseClaimNextWorkflow(t *testing.T) {
	bd := buildBDUnderTest(t)
	for _, engine := range []string{"embedded", "server"} {
		t.Run(engine, func(t *testing.T) {
			work, home := t.TempDir(), t.TempDir()
			const scope = "https://example.invalid/close-claim-next/"
			initArgs := []string{"init", "--graph-mode", "link", "--scope-url", scope, "--skip-hooks", "--skip-agents", "--non-interactive"}
			if engine == "server" {
				port := os.Getenv("BEADS_GRAPH_TEST_SERVER_PORT")
				if port == "" {
					t.Skip("set BEADS_GRAPH_TEST_SERVER_PORT for ordinary shared-server qualification")
				}
				initArgs = append(initArgs, "--server", "--external", "--server-host", "127.0.0.1", "--server-port", port, "--server-user", "root")
			}
			call := func(args ...string) string {
				t.Helper()
				return graphPolicyCLI(t, bd, work, home, nil, "", append(args, "--json")...)
			}
			call(initArgs...)
			call("create", "Blocking prerequisite", "--id", "blocker", "--priority", "3")
			call("create", "Next work", "--id", "dependent", "--priority", "0")
			call("create", "Spare work", "--id", "spare", "--priority", "1")
			call("dep", "add", "dependent", "blocker")
			dependentBefore := graphMixedResult[graphstore.IssueRecord](t, call("show", "dependent"))
			spareBefore := graphMixedResult[graphstore.IssueRecord](t, call("show", "spare"))
			blockerBefore := call("versions", "blocker")
			type claimResult struct {
				Closed  []graphstore.IssueRecord `json:"closed"`
				Claimed graphstore.IssueRecord   `json:"claimed"`
			}
			first := graphMixedResult[claimResult](t, call("close", "blocker", "--claim-next", "--session", "claim-session", "--reason", "Done"))
			if len(first.Closed) != 1 || first.Closed[0].Properties.Status != types.StatusClosed ||
				first.Closed[0].Properties.ClosedBySession != "claim-session" || first.Claimed.ID != scope+"beads/dependent" ||
				first.Claimed.Properties.Status != types.StatusInProgress || first.Claimed.Properties.Assignee == "" ||
				first.Claimed.Properties.LeaseExpiresAt == nil || first.Claimed.Properties.HeartbeatAt == nil ||
				first.Claimed.Properties.LeaseExpiresAt.Sub(*first.Claimed.Properties.HeartbeatAt) != 5*time.Minute ||
				first.Claimed.Revision == dependentBefore.Revision {
				t.Fatalf("close did not atomically claim newly ready Issue: %+v", first)
			}
			if call("versions", "blocker") == blockerBefore {
				t.Fatal("close did not retain the native Issue successor")
			}
			closedVersions, dependentVersions := call("versions", "blocker"), call("versions", "dependent")
			if retry := graphMixedResult[[]graphstore.IssueRecord](t, call("close", "blocker", "--claim-next", "--reason", "Ignored")); len(retry) != 1 || !reflect.DeepEqual(retry[0], first.Closed[0]) {
				t.Fatalf("already-closed retry changed Issue or claimed work: %+v", retry)
			}
			if call("versions", "blocker") != closedVersions || call("versions", "dependent") != dependentVersions ||
				!reflect.DeepEqual(graphMixedResult[graphstore.IssueRecord](t, call("show", "spare")), spareBefore) {
				t.Fatal("no-op retry minted a version or claimed spare work")
			}
			call("create", "Higher priority spare", "--id", "next", "--priority", "0")
			nextBefore := graphMixedResult[graphstore.IssueRecord](t, call("show", "next"))
			partial := func(wantClaim string, args ...string) string {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, bd, append(args, "--json")...)
				cmd.Dir = work
				cmd.Env = []string{
					"PATH=" + os.Getenv("PATH"), "HOME=" + home,
					"TMPDIR=" + os.TempDir(), "TMP=" + os.TempDir(), "TEMP=" + os.TempDir(),
					"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"), "XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
					"GIT_CONFIG_GLOBAL=" + filepath.Join(home, "missing-gitconfig"), "GIT_CONFIG_NOSYSTEM=1",
					"BD_DISABLE_METRICS=1", "BD_DISABLE_EVENT_FLUSH=1", "BD_NON_INTERACTIVE=1",
					"BEADS_DOLT_AUTO_START=0", "DOLT_METRICS_DISABLED=1", "NO_COLOR=1",
				}
				var stdout, stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				err := cmd.Run()
				if ctx.Err() != nil {
					t.Fatalf("partial close timed out: %v", ctx.Err())
				}
				if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 || !strings.Contains(stderr.String(), `"code":"not_found"`) {
					t.Fatalf("partial close did not report refused sibling: %v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
				}
				if wantClaim != "" && (!strings.Contains(stderr.String(), `"code":"partial_close_claim_retained"`) || !strings.Contains(stderr.String(), wantClaim)) {
					t.Fatalf("partial failure did not name its retained claim: %s", stderr.String())
				}
				return stdout.String()
			}
			mixed := graphMixedResult[claimResult](t, partial(scope+"beads/next", "close", "spare", "missing", "--claim-next", "--reason", "Done"))
			if len(mixed.Closed) != 1 || mixed.Closed[0].ID != scope+"beads/spare" || mixed.Closed[0].Properties.Status != types.StatusClosed ||
				mixed.Claimed.ID != scope+"beads/next" || mixed.Claimed.Revision == nextBefore.Revision || mixed.Claimed.Properties.Status != types.StatusInProgress {
				t.Fatalf("partial close lost the committed close or claim: %+v", mixed)
			}
			if got := graphMixedResult[graphstore.IssueRecord](t, call("show", "next")); !reflect.DeepEqual(got, mixed.Claimed) {
				t.Fatal("partial failure hid or rolled back its retained claim")
			}
			spareVersions, nextVersions := call("versions", "spare"), call("versions", "next")
			repeated := graphMixedResult[[]graphstore.IssueRecord](t, partial("", "close", "spare", "missing", "--claim-next"))
			if len(repeated) != 1 || call("versions", "spare") != spareVersions || call("versions", "next") != nextVersions {
				t.Fatalf("partial retry claimed again or wrote a version: %+v", repeated)
			}
			call("create", "Last unassigned work", "--id", "terminal")
			nothingReady := graphMixedResult[[]graphstore.IssueRecord](t, call("close", "terminal", "--claim-next"))
			if len(nothingReady) != 1 || nothingReady[0].Properties.Status != types.StatusClosed {
				t.Fatalf("no-ready close did not commit without a claim: %+v", nothingReady)
			}
		})
	}
}
