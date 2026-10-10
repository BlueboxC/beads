//go:build cgo

package main

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/graphstore"
)

// graphListedBead is one Bead of a structured list: enough to check the order
// from the records' own recorded times instead of from their IDs.
type graphListedBead struct{ ID, Type, RecordedAt string }

// graphListDecode reads the Beads of a structured list in the order listed.
func graphListDecode(t *testing.T, output string) (beads []graphListedBead, more bool) {
	t.Helper()
	page := graphMixedResult[struct {
		Items []struct {
			ID, Type    string
			Attribution struct {
				RecordedAt string `json:"recordedAt"`
			} `json:"attribution"`
		} `json:"items"`
		HasMore bool `json:"hasMore"`
	}](t, output)
	for _, item := range page.Items {
		beads = append(beads, graphListedBead{item.ID, item.Type, item.Attribution.RecordedAt})
	}
	return beads, page.HasMore
}

// graphListNewestFirst reports whether the Beads are in the order every Bead
// list promises: newest recorded change first, equal instants by canonical ID.
// Times are compared as instants, and IDs are ASCII in these workspaces.
func graphListNewestFirst(t *testing.T, beads []graphListedBead) bool {
	t.Helper()
	instant := func(bead graphListedBead) time.Time {
		at, err := time.Parse(time.RFC3339Nano, bead.RecordedAt)
		if err != nil {
			t.Fatalf("%s has no readable recorded time: %v", bead.ID, err)
		}
		return at
	}
	want := slices.Clone(beads)
	slices.SortFunc(want, func(a, b graphListedBead) int {
		if c := instant(b).Compare(instant(a)); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return slices.Equal(beads, want)
}

// graphListHumanPaths names the Beads of a human list in row order: each Bead
// row starts with its local ID after a two-space indent.
func graphListHumanPaths(output string) []string {
	paths := []string{}
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "  ") {
			paths = append(paths, strings.Fields(line)[0])
		}
	}
	return paths
}

// graphListRefusal runs a list that must be refused as unavailable and returns
// the raw diagnostic, which names what to change.
func graphListRefusal(t *testing.T, bd, work, home string, extraEnv []string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	cmd := graphMemoryReadCommand(ctx, bd, work, home, args...)
	cmd.Env = append(cmd.Env, extraEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if ctx.Err() != nil || !errors.As(err, &exit) || exit.ExitCode() != 5 || stdout.Len() != 0 || !strings.HasPrefix(stderr.String(), "capability_unavailable: ") {
		t.Fatalf("%v with %v was not refused as unavailable: err=%v stdout=%q stderr=%q", args, extraEnv, err, stdout.Bytes(), stderr.Bytes())
	}
	return stderr.String()
}

// One order for every Bead, newest recorded change first, and BEADS_MAX_ROWS
// bounds the page that would be returned rather than the Beads that match.
func TestGraphPreviewListOrderAndCapWorkflow(t *testing.T) {
	bd := buildBDUnderTest(t)
	for _, engine := range []string{"embedded", "server"} {
		t.Run(engine, func(t *testing.T) {
			const scope = "https://example.invalid/order/"
			work, home := graphListWorkspace(t, bd, engine, scope)
			call := func(args ...string) string {
				t.Helper()
				return graphPolicyCLI(t, bd, work, home, nil, "", append(args, "--json")...)
			}
			structured := func(extraEnv []string, extra ...string) ([]graphListedBead, bool) {
				t.Helper()
				return graphListDecode(t, graphPolicyCLI(t, bd, work, home, extraEnv, "", append([]string{"list", "--format", "records-json"}, extra...)...))
			}
			pathsOf := func(beads []graphListedBead) []string {
				paths := []string{}
				for _, bead := range beads {
					paths = append(paths, strings.TrimPrefix(bead.ID, scope))
				}
				return paths
			}
			expect := func(what string, beads []graphListedBead, more bool, want []string, wantMore bool) {
				t.Helper()
				if got := pathsOf(beads); !slices.Equal(got, want) || more != wantMore || !graphListNewestFirst(t, beads) {
					t.Fatalf("%s: listed %v more=%t, want %v more=%t newest first", what, got, more, want, wantMore)
				}
			}

			// A Memory, an Issue and a Memory, each recorded by its own process.
			call("remember", "first body", "--id", "beads/first", "--title", "First")
			call("create", "Second", "--id", "beads/second")
			call("remember", "third body", "--id", "beads/third", "--title", "Third")
			beads, more := structured(nil)
			expect("creation order", beads, more, []string{"beads/third", "beads/second", "beads/first"}, false)

			// An edit is a new recorded version, so the oldest Bead moves to the front.
			first := graphMixedResult[graphstore.Record](t, call("show", "beads/first"))
			call("update", "beads/first", "--properties", `{"title":"First, revised","body":"first body"}`, "--if-revision", first.Revision)
			edited := []string{"beads/first", "beads/third", "beads/second"}
			beads, more = structured(nil)
			expect("after editing the oldest", beads, more, edited, false)
			beads, more = structured(nil, "--limit", "2")
			expect("--limit 2 keeps the two newest", beads, more, edited[:2], true)
			beads, more = structured(nil, "--bead-type", "types/preview-memory-v2")
			expect("Memory Type", beads, more, []string{"beads/first", "beads/third"}, false)
			beads, more = structured(nil, "--bead-type", "types/preview-issue-v2")
			expect("Issue Type", beads, more, []string{"beads/second"}, false)
			if got := graphListHumanPaths(graphPolicyCLI(t, bd, work, home, nil, "", "list")); !slices.Equal(got, edited) {
				t.Fatalf("human rows %v follow a different order than the structured list %v", got, edited)
			}

			// The cap counts the page: three Beads fit a cap of three, a limit at
			// the cap never trips it, and an unlimited page over it is refused with
			// what to change.
			beads, more = structured([]string{"BEADS_MAX_ROWS=3"})
			expect("cap of three", beads, more, edited, false)
			beads, more = structured([]string{"BEADS_MAX_ROWS=2"}, "--limit", "2")
			expect("limit at the cap", beads, more, edited[:2], true)
			diagnostic := graphListRefusal(t, bd, work, home, []string{"BEADS_MAX_ROWS=2"}, "list")
			for _, want := range []string{"BEADS_MAX_ROWS=2", "page of 3 Beads", "--limit"} {
				if !strings.Contains(diagnostic, want) {
					t.Fatalf("refusal must name %q: %s", want, diagnostic)
				}
			}
			// Structured output refuses with the same typed code.
			graphPolicyCLI(t, bd, work, home, []string{"BEADS_MAX_ROWS=2"}, "capability_unavailable", "list", "--format", "records-json")
			graphListRefusal(t, bd, work, home, []string{"BEADS_MAX_ROWS=2"}, "list", "--limit", "3")
		})
	}
}
