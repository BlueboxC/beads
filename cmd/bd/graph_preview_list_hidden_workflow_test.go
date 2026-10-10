//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/graphstore"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// graphListWorkspace initializes a graph workspace on one engine through normal
// initialization, so every later command is a separate installed process.
func graphListWorkspace(t *testing.T, bd, engine, scope string) (work, home string) {
	t.Helper()
	work, home = t.TempDir(), t.TempDir()
	args := []string{"init", "--graph-mode", "link", "--scope-url", scope, "--skip-hooks", "--skip-agents", "--non-interactive", "--json"}
	if engine == "server" {
		port := os.Getenv("BEADS_GRAPH_TEST_SERVER_PORT")
		if port == "" {
			t.Skip("set BEADS_GRAPH_TEST_SERVER_PORT for ordinary shared-server CLI qualification")
		}
		args = append(args, "--server", "--external", "--server-host", "127.0.0.1", "--server-port", port, "--server-user", "root")
	}
	graphPolicyCLI(t, bd, work, home, nil, "", args...)
	return work, home
}

// graphListConfig appends workspace configuration. Initialization writes none in
// a graph workspace, and the graph CLI has no command that sets any.
func graphListConfig(t *testing.T, work, yaml string) {
	t.Helper()
	path := filepath.Join(work, ".beads", "config.yaml")
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	body := string(existing)
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	if err := os.WriteFile(path, []byte(body+yaml), 0o600); err != nil {
		t.Fatal(err)
	}
}

// graphListSeedIssue creates an Issue through the controlled writer between two
// installed processes, for a state no admitted command can author. Close it
// before the next process opens the workspace, embedded mode included.
func graphListSeedIssue(t *testing.T, work, path string, request publicops.CreateRequest) {
	t.Helper()
	cfg, err := configfile.LoadForDiscovery(filepath.Join(work, ".beads"))
	if err != nil || cfg == nil {
		t.Fatalf("read initialized binding: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	s, err := graphstore.OpenExisting(ctx, graphstore.Options{
		Backend: cfg.DoltMode, DataDir: filepath.Join(cfg.GraphWorkspace, "embeddeddolt"), Database: cfg.DoltDatabase, Branch: "main",
		Binding:    graphstore.Binding{WorkspaceID: cfg.GraphWorkspace, ScopeURL: cfg.GraphScopeURL, AuthorityID: cfg.GraphAuthorityID, SchemaVersion: cfg.GraphSchemaVersion},
		ServerHost: cfg.DoltServerHost, ServerPort: cfg.DoltServerPort, ServerUser: cfg.DoltServerUser, ServerSocket: cfg.DoltServerSocket, ServerTLS: cfg.DoltServerTLS,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := s.CreateIssue(ctx, path, request); err != nil {
		t.Fatal(err)
	}
}

// graphListBeads runs the structured Bead list and returns its page: the
// canonical Bead paths, without the scope, in the order listed.
func graphListBeads(t *testing.T, bd, work, home, scope string, extra ...string) (paths []string, more bool) {
	t.Helper()
	beads, more := graphListDecode(t, graphPolicyCLI(t, bd, work, home, nil, "", append([]string{"list", "--format", "records-json"}, extra...)...))
	for _, bead := range beads {
		paths = append(paths, strings.TrimPrefix(bead.ID, scope))
	}
	return paths, more
}

func graphListSorted(paths []string) []string {
	sorted := slices.Clone(paths)
	slices.Sort(sorted)
	return sorted
}

// Closed and pinned Issues are hidden from the Bead list unless --all, which
// also lifts the default row limit; an explicit --limit still wins. The CLI
// cannot author a pinned Issue, so the test seeds one between processes.
func TestGraphPreviewListHiddenIssuesWorkflow(t *testing.T) {
	bd := buildBDUnderTest(t)
	for _, engine := range []string{"embedded", "server"} {
		t.Run(engine, func(t *testing.T) {
			const scope = "https://example.invalid/hidden/"
			work, home := graphListWorkspace(t, bd, engine, scope)
			call := func(args ...string) string {
				t.Helper()
				return graphPolicyCLI(t, bd, work, home, nil, "", append(args, "--json")...)
			}
			list := func(extra ...string) ([]string, bool) {
				t.Helper()
				return graphListBeads(t, bd, work, home, scope, extra...)
			}
			human := func(extra ...string) string {
				t.Helper()
				return graphPolicyCLI(t, bd, work, home, nil, "", append([]string{"list"}, extra...)...)
			}
			// A piped process has no default row limit; a configured one is how the
			// default limit becomes visible to it.
			graphListConfig(t, work, "list:\n  limit: 2\n")
			call("remember", "private body", "--id", "beads/plan", "--title", "Plan")
			call("create", "Open work", "--id", "beads/open", "--priority", "1")
			call("create", "Finished work", "--id", "beads/done", "--priority", "2")
			call("close", "beads/done")
			graphListSeedIssue(t, work, "beads/pin", publicops.CreateRequest{Actor: "test-author", Issue: &publicops.Issue{
				Title: "Pinned note", Description: "kept", Status: types.StatusPinned, IssueType: types.TypeTask, Priority: 3,
			}})

			expect := func(what string, got []string, more bool, want []string, wantMore bool) {
				t.Helper()
				if !slices.Equal(graphListSorted(got), graphListSorted(want)) || more != wantMore {
					t.Fatalf("%s: listed %v more=%t, want %v more=%t", what, got, more, want, wantMore)
				}
			}
			everything := []string{"beads/done", "beads/open", "beads/pin", "beads/plan"}
			issues := []string{"beads/done", "beads/open", "beads/pin"}

			got, more := list()
			expect("bare list hides the closed and the pinned Issue", got, more, []string{"beads/open", "beads/plan"}, false)
			got, more = list("--bead-type", "types/preview-issue-v2")
			expect("Issue Type hides them too", got, more, []string{"beads/open"}, false)
			got, more = list("--bead-type", "types/preview-memory-v2")
			expect("a Memory is never hidden", got, more, []string{"beads/plan"}, false)

			got, more = list("--all")
			expect("--all shows them and lifts the configured default limit", got, more, everything, false)
			got, more = list("--all", "--bead-type", "types/preview-issue-v2")
			expect("--all with the Issue Type", got, more, issues, false)
			got, more = list("--all", "--limit", "3")
			if len(got) != 3 || !more {
				t.Fatalf("an explicit --limit must win over --all: listed %v more=%t", got, more)
			}
			got, more = list("--limit", "1")
			if len(got) != 1 || !more {
				t.Fatalf("a limit under the shown Beads must report more: listed %v more=%t", got, more)
			}

			// The human rows carry an Issue's status and priority, and the hint
			// offers --all only while it would still help.
			rows := human("--all")
			for _, want := range []string{"  beads/done  Issue   closed  P2  Finished work\n", "  beads/open  Issue   open  P1  Open work\n", "  beads/pin  Issue   pinned  P3  Pinned note\n", "  beads/plan  Memory  Plan\n"} {
				if !strings.Contains(rows, want) {
					t.Fatalf("human rows lack %q: %s", want, rows)
				}
			}
			if out := human("--all", "--limit", "3"); !strings.Contains(out, "More Beads exist; increase --limit within preview bounds.") || strings.Contains(out, "use --all") {
				t.Fatalf("hint under --all must not offer --all: %s", out)
			}
			if out := human("--limit", "1"); !strings.Contains(out, "More Beads exist; increase --limit or use --all within preview bounds.") {
				t.Fatalf("hint without --all must offer it: %s", out)
			}
			if out := human(); !strings.HasPrefix(out, "Beads (2; more: false; graph preview)\n") || strings.Contains(out, "beads/done") || strings.Contains(out, "beads/pin") || !strings.Contains(out, "  beads/plan  Memory  Plan\n") {
				t.Fatalf("default human list: %s", out)
			}
		})
	}
}
