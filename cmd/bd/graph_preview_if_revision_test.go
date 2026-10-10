//go:build cgo

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/storage/graphstore"
)

// --if-revision is one flag with two meanings, and which one applies depends on
// the workspace. In a graph_mode link workspace it is an opaque observed graph
// token. Everywhere else on update, delete, close and assign it is the
// compare-and-swap upstream added in #7203: a decimal bead revision whose
// mismatch exits 13, which gascity's bdstore_conditional.go classifier reads.
//
// Upstream registers the flag on those four verbs, so the preview registers it
// only where upstream has none (remember, forget, graph unlink) and its link
// mode handlers read upstream's registration. Registering it twice panics at
// init, so these tests also keep the whole cmd/bd test binary startable.

// graphIfRevisionGuardMismatchExit is a literal on purpose: the contract with
// gascity is the number, not whatever name the constant for it happens to have.
const graphIfRevisionGuardMismatchExit = 13

// graphIfRevisionRun runs one disposable CLI process and returns both streams
// with its exit status. graphPolicyCLI cannot stand in: it demands a typed
// refusal on stderr, and a refused upstream --if-revision write leads with a
// human line there, so the status itself is part of what these tests pin.
func graphIfRevisionRun(t *testing.T, bd, work, home string, args ...string) (stdout, stderr string, exit int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	cmd := graphMemoryReadCommand(ctx, bd, work, home, args...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("CLI did not complete within deadline: %v args=%v\n%s", ctx.Err(), args, errOut.String())
	}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		exit = exitErr.ExitCode()
	default:
		t.Fatalf("could not run bd %v: %v", args, err)
	}
	return out.String(), errOut.String(), exit
}

// graphIfRevisionRefused asserts a typed graph refusal: exactly this exit
// status, nothing on stdout, and a stderr that is one {"code": ...} document.
func graphIfRevisionRefused(t *testing.T, bd, work, home string, wantExit int, wantCode string, args ...string) {
	t.Helper()
	stdout, stderr, exit := graphIfRevisionRun(t, bd, work, home, append(append([]string(nil), args...), "--json")...)
	var diagnostic struct {
		Code      string `json:"code"`
		Retryable bool   `json:"retryable"`
	}
	if exit != wantExit || stdout != "" || json.Unmarshal([]byte(stderr), &diagnostic) != nil || diagnostic.Code != wantCode || diagnostic.Retryable {
		t.Fatalf("bd %v: want exit %d with a non-retryable typed %s refusal, got exit %d\nstdout: %s\nstderr: %s", args, wantExit, wantCode, exit, stdout, stderr)
	}
}

// This fork retains the v1.3.1 ordinary CLI. Opaque revision flags are
// explicitly graph-only; unsupported ordinary writes must fail before opening.
func TestGraphPreviewIfRevisionFlagOwnership(t *testing.T) {
	for _, cmd := range []*cobra.Command{updateCmd, deleteCmd, closeCmd, deferCmd, undeferCmd, rememberCmd, forgetCmd, graphUnlinkCmd} {
		if flag := cmd.Flags().Lookup("if-revision"); flag == nil {
			t.Errorf("bd %s lost its graph revision guard", cmd.Name())
		}
	}
}
func TestGraphPreviewIfRevisionOutsideLinkModeRefuses(t *testing.T) {
	bd := buildBDUnderTest(t)
	work, home := t.TempDir(), t.TempDir()
	graphPolicyCLI(t, bd, work, home, nil, "", "init", "--prefix", "ifrev", "--skip-hooks", "--skip-agents", "--non-interactive", "--json")
	before := legacyUpgradeTreeDigest(t, work)
	for _, args := range [][]string{{"update", "ifrev-missing", "--priority", "3", "--if-revision", "observed"}, {"delete", "ifrev-missing", "--force", "--if-revision", "1"}, {"close", "ifrev-missing", "--if-revision", "1"}} {
		graphIfRevisionRefused(t, bd, work, home, 5, "capability_unavailable", args...)
	}
	if after := legacyUpgradeTreeDigest(t, work); after != before {
		t.Fatal("refused graph-only flags changed ordinary workspace")
	}
}

func TestGraphPreviewIfRevisionInLinkModeStaysGraphToken(t *testing.T) {
	bd := buildBDUnderTest(t)
	work, home := t.TempDir(), t.TempDir()
	call := func(args ...string) string {
		t.Helper()
		return graphPolicyCLI(t, bd, work, home, nil, "", append(args, "--json")...)
	}
	refuse := func(code string, args ...string) {
		t.Helper()
		graphPolicyCLI(t, bd, work, home, nil, code, append(args, "--json")...)
	}
	call("init", "--graph-mode", "link", "--scope-url", "https://example.invalid/if-revision/", "--skip-hooks", "--skip-agents", "--non-interactive")
	memory := graphMixedResult[graphstore.Record](t, call("remember", "Guarded body", "--id", "beads/plan", "--title", "Plan"))
	const replacement = `{"title":"Plan","body":"Replaced body"}`
	before := call("show", "beads/plan")
	for _, token := range []string{"observed", "7"} {
		refuse("revision_conflict", "update", "beads/plan", "--properties", replacement, "--if-revision", token)
		refuse("revision_conflict", "delete", "beads/plan", "--if-revision", token)
		refuse("revision_conflict", "delete", "beads/plan", "--force", "--if-revision", token)
	}
	if call("show", "beads/plan") != before {
		t.Fatal("a refused graph token changed the Memory")
	}

	replaced := graphMixedResult[graphstore.MemoryMutationResult](t, call("update", "beads/plan", "--properties", replacement, "--if-revision", memory.Revision))
	if !replaced.Changed || replaced.Memory.Revision == memory.Revision {
		t.Fatal("the observed graph token did not guard-and-apply the update")
	}
	deleted := graphMixedResult[graphstore.MemoryDeleteResult](t, call("delete", "beads/plan", "--force", "--if-revision", replaced.Memory.Revision))
	if !deleted.Deleted || deleted.Preview {
		t.Fatal("the observed graph token did not guard-and-apply the deletion")
	}
}

// In graph mode close now interprets --if-revision as the opaque observed graph
// Issue token. Assign has no graph route and still refuses the flag rather than
// quietly dropping its upstream numeric guard.
func TestGraphPreviewCloseIfRevisionUsesGraphToken(t *testing.T) {
	bd := buildBDUnderTest(t)
	work, home := t.TempDir(), t.TempDir()
	call := func(args ...string) string {
		t.Helper()
		return graphPolicyCLI(t, bd, work, home, nil, "", append(args, "--json")...)
	}
	call("init", "--graph-mode", "link", "--scope-url", "https://example.invalid/close-if-revision/", "--skip-hooks", "--skip-agents", "--non-interactive")
	call("create", "Close guard", "--id", "beads/close-guard")
	before := call("show", "beads/close-guard")
	for _, args := range [][]string{
		{"close", "beads/close-guard", "--if-revision", "1"},
		{"close", "beads/close-guard", "--reason", "Done", "--if-revision", "1"},
	} {
		graphIfRevisionRefused(t, bd, work, home, 4, "revision_conflict", args...)
	}
	graphIfRevisionRefused(t, bd, work, home, 5, "capability_unavailable", "assign", "beads/close-guard", "alice", "--if-revision", "1")
	if call("show", "beads/close-guard") != before {
		t.Fatal("a refused guarded close changed the Issue")
	}
	observed := graphMixedResult[graphstore.IssueRecord](t, before)
	if closed := graphMixedResult[graphstore.IssueMutationResult](t, call("close", "beads/close-guard", "--reason", "Done", "--if-revision", observed.Revision)); !closed.Changed {
		t.Fatal("a close with the observed graph revision did not apply")
	}
}
