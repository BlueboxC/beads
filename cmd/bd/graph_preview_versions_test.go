//go:build cgo

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/graphstore"
)

// These are the CLI-level tests for graph-mode `bd versions` (and `bd history`
// as its alias there). The unit suites around it cover the store reader and the
// projection shape; nothing exercised runGraphPreviewVersions through an
// installed binary, which is how an undefined symbol in that file shipped as a
// compile break rather than a failing test.
//
// Every assertion here drives the real bd process in a throwaway graph
// workspace, matching graph_preview_policy_test.go and the other
// graph_preview_*_test.go end-to-end files.

// graphVersionsRow is the typed view of one `--json` row. It is deliberately
// NOT graphstore.VersionRow: these tests pin the CLI's wire contract, which
// graph_preview_versions.go builds member by member precisely so it cannot
// drift with the store's struct tags. Sharing the store type would make the two
// agree by construction and test nothing.
type graphVersionsRow struct {
	Ordinal     int64  `json:"local_revision"`
	Version     string `json:"version"`
	ChangeAt    string `json:"change_at"`
	Actor       string `json:"actor"`
	Attribution string `json:"attribution"`
}

// The release contract must hold on both installed engines. The longer
// projection test in this file pins every row field; this one proves that a
// removed Link's listed addresses remain usable after a shared-server commit.
func TestGraphPreviewRemovedLinkVersionsTwoEngines(t *testing.T) {
	bd := buildBDUnderTest(t)
	for _, engine := range []string{"embedded", "server"} {
		t.Run(engine, func(t *testing.T) {
			work, home := t.TempDir(), t.TempDir()
			const scope = "https://example.invalid/citable-versions/"
			call := func(args ...string) string {
				t.Helper()
				return graphVersionsOK(t, bd, work, home, append(args, "--json")...)
			}
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
			call(initArgs...)
			call("create", "Source", "--id", "source")
			call("create", "Target", "--id", "target")
			call("link", "source", "target", "--id", "links/edge", "--resource-type", scope+"types/preview-related-v2")
			call("update", "links/edge", "--properties", `{"note":"changed"}`)
			removed := graphMixedResult[graphstore.LinkDeleteResult](t, call("unlink", "links/edge", "--unconditional"))
			listed := call("versions", "links/edge")
			_, kind, rows, members := graphVersionsListed(t, listed)
			if kind != "link" || len(rows) != 2 {
				t.Fatalf("removed Link listed %q with %d citable versions: %s", kind, len(rows), listed)
			}
			graphVersionsAssertKeys(t, engine, members)
			for _, row := range rows {
				if row.Version == removed.Link.Version {
					t.Fatalf("deletion marker became a version: %+v", row)
				}
				call("show", "links/edge", "--version", row.Version)
			}
			if got := call("history", "links/edge"); got != listed {
				t.Fatalf("history alias differs from versions: history=%s versions=%s", got, listed)
			}
			_, stderr, code := graphVersionsProcess(t, bd, work, home,
				"show", "links/edge", "--version", removed.Link.Version, "--json")
			if code == 0 || !strings.Contains(stderr, "gone") {
				t.Fatalf("private deletion token unexpectedly citable: code=%d stderr=%q", code, stderr)
			}
		})
	}
}

// graphVersionsWireKeys is the COMPLETE member set of one row, sorted. The set
// is asserted exactly rather than by presence, so an added or renamed member
// fails here instead of reaching a script.
//
// `revision` is the absence that matters. Graph records spell the opaque
// citable token `revision`, and a native Issue spells its row-lock CAS token
// `revision`, so emitting it beside `local_revision` would invite a caller to
// compare a store-local ordering key against an address. Two clones can hold the same
// ordinal for different states, so that comparison is wrong rather than merely
// confusing.
var graphVersionsWireKeys = []string{"actor", "attribution", "change_at", "local_revision", "version"}

// graphVersionsHumanRow matches one rendered row line: ordinal, microsecond
// timestamp, actor, attribution. The token lives on its own following line, so
// this deliberately does not match it.
var graphVersionsHumanRow = regexp.MustCompile(`^  (\d+) +(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{6}) +(\S+) +(\S+)$`)

// graphVersionsProcess runs one installed bd process and returns stdout,
// stderr and the exit code SEPARATELY. Keeping the three apart is the point: the
// not_found regression below is about how many diagnostics landed on stderr and
// which exit code accompanied them, and a combined-output helper would hide
// exactly that.
func graphVersionsProcess(t *testing.T, bd, work, home string, args ...string) (string, string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	cmd := graphMemoryReadCommand(ctx, bd, work, home, args...)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("bd %v exceeded deadline: %v stderr=%s", args, ctx.Err(), stderr.String())
	}
	code := 0
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		code = exit.ExitCode()
	case err != nil:
		t.Fatalf("bd %v did not run: %v stderr=%s", args, err, stderr.String())
	}
	return out.String(), stderr.String(), code
}

// graphVersionsOK requires a clean run: a graph read that succeeds writes
// nothing to stderr, so a non-empty stderr beside exit 0 is itself a failure.
func graphVersionsOK(t *testing.T, bd, work, home string, args ...string) string {
	t.Helper()
	out, stderr, code := graphVersionsProcess(t, bd, work, home, args...)
	if code != 0 || stderr != "" {
		t.Fatalf("bd %v failed: code=%d stderr=%s stdout=%s", args, code, stderr, out)
	}
	return out
}

// graphVersionsListed decodes one `--json` listing twice over: into raw member
// maps so the key set can be compared exactly, and into the typed row for value
// assertions. Decoding only into the struct would silently tolerate an extra or
// renamed member, which is the drift the key-set test exists to catch.
func graphVersionsListed(t *testing.T, raw string) (string, string, []graphVersionsRow, []map[string]json.RawMessage) {
	t.Helper()
	var envelope struct {
		SchemaVersion int  `json:"schemaVersion"`
		Preview       bool `json:"preview"`
		Result        struct {
			Resource string                       `json:"resource"`
			Kind     string                       `json:"kind"`
			Versions []map[string]json.RawMessage `json:"versions"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		t.Fatalf("listing is not the graph preview envelope: %v\n%s", err, raw)
	}
	if envelope.SchemaVersion != 1 || !envelope.Preview {
		t.Fatalf("listing lost its preview envelope: %s", raw)
	}
	if len(envelope.Result.Versions) == 0 {
		// An allocated subject always has at least version 1, because creation
		// writes it in the same transaction as the catalog row. An empty list
		// is therefore never a correct answer, only a wrong one that reads as
		// reassuring.
		t.Fatalf("empty version list is never a valid answer: %s", raw)
	}
	rows := make([]graphVersionsRow, 0, len(envelope.Result.Versions))
	for i, member := range envelope.Result.Versions {
		encoded, err := json.Marshal(member)
		if err != nil {
			t.Fatal(err)
		}
		var row graphVersionsRow
		if err := json.Unmarshal(encoded, &row); err != nil {
			t.Fatalf("row %d is not a version row: %v\n%s", i, err, raw)
		}
		rows = append(rows, row)
	}
	return envelope.Result.Resource, envelope.Result.Kind, rows, envelope.Result.Versions
}

// graphVersionsAssertKeys pins the wire contract one row at a time.
func graphVersionsAssertKeys(t *testing.T, label string, members []map[string]json.RawMessage) {
	t.Helper()
	for i, member := range members {
		if _, bad := member["revision"]; bad {
			// Named before the set comparison so this specific regression
			// fails with its reason rather than as a bare set mismatch.
			t.Fatalf("%s row %d emitted `revision`: a store-local ordinal is not a citable address", label, i)
		}
		got := make([]string, 0, len(member))
		for key := range member {
			got = append(got, key)
		}
		sort.Strings(got)
		if !reflect.DeepEqual(got, graphVersionsWireKeys) {
			t.Fatalf("%s row %d wire members drifted: got %v want %v", label, i, got, graphVersionsWireKeys)
		}
	}
}

// graphVersionsAssertOrder pins newest-first ordering. Strictly descending
// matters more than it looks: ordinal is the ordering authority, so a repeated
// ordinal means two rows claim the same place in the history, and a run that is
// merely non-increasing would accept that.
func graphVersionsAssertOrder(t *testing.T, label string, rows []graphVersionsRow) {
	t.Helper()
	for i := 1; i < len(rows); i++ {
		if rows[i-1].Ordinal <= rows[i].Ordinal {
			t.Fatalf("%s is not strictly newest-first at row %d: %d then %d", label, i, rows[i-1].Ordinal, rows[i].Ordinal)
		}
	}
	if oldest := rows[len(rows)-1].Ordinal; oldest != 1 {
		// Creation IS version 1 on every plane, so the oldest listed row is
		// ordinal 1 unless the listing lost its tail. A truncated history that
		// still looked ordered is the one failure a History reader must never
		// present as complete.
		t.Fatalf("%s oldest ordinal is %d, not 1: the creating version is missing", label, oldest)
	}
}

// graphVersionsHumanRows returns the rendered row lines, parsed into the four
// columns, in the order they were printed.
func graphVersionsHumanRows(t *testing.T, label, human string) [][]string {
	t.Helper()
	var parsed [][]string
	for _, line := range strings.Split(human, "\n") {
		if match := graphVersionsHumanRow.FindStringSubmatch(line); match != nil {
			parsed = append(parsed, match[1:])
		}
	}
	if len(parsed) == 0 {
		t.Fatalf("%s rendered no parseable rows:\n%s", label, human)
	}
	return parsed
}

// TestGraphPreviewVersionsCLI drives graph-mode `bd versions` end to end.
//
// ONE embedded-Dolt workspace serves every subtest. Initializing a graph
// workspace costs tens of seconds, and the subjects below are independent paths
// that cannot interfere with each other's histories, so sharing the store is
// the difference between a usable test and an unusable one. The subtests are
// read-only; all writing happens once, up front.
func TestGraphPreviewVersionsCLI(t *testing.T) {
	bd := buildBDUnderTest(t)
	work, home := t.TempDir(), t.TempDir()
	const (
		scope = "https://example.org/team/"
		// The informational Link Type URL for this scope. `bd link` routes to
		// dependency creation for any other value, so this constant decides
		// which plane the Link fixture lands on.
		relatedType = scope + "types/preview-related-v2"
		// An explicit actor keeps the human ACTOR column a single token, so the
		// column regex can tell actor and attribution apart, and makes the
		// Issue plane's derived attribution_status deterministic: a non-empty
		// actor is "claimed", an empty one would be "unknown".
		author = "versions-author"
	)
	// run takes its own *testing.T rather than closing over the outer one: a
	// subtest runs on its own goroutine, and calling the parent's Fatalf from
	// there aborts the wrong test and loses the message.
	run := func(t *testing.T, args ...string) string {
		t.Helper()
		return graphVersionsOK(t, bd, work, home, args...)
	}
	run(t, "init", "--graph-mode", "link", "--scope-url", scope, "--skip-hooks", "--skip-agents", "--non-interactive", "--json")

	// Issue plane: created, then renamed, so there are two versions to order
	// and a second row to carry native attribution.
	run(t, "create", "Issue subject", "--id", "beads/issue", "--actor", author, "--json")
	run(t, "create", "Link target", "--id", "beads/target", "--actor", author, "--json")
	run(t, "update", "beads/issue", "--title", "Issue subject renamed", "--actor", author, "--json")
	// Memory plane: created, then replaced. Memory records no attribution
	// status at all, which is a different thing from recording an empty one.
	run(t, "remember", "Memory body", "--id", "beads/memory", "--title", "Memory subject", "--actor", author, "--json")
	run(t, "update", "beads/memory", "--properties", `{"title":"Memory subject renamed","body":"Memory body again"}`,
		"--actor", author, "--json")
	// Link plane: created, changed, then unlinked. The private deletion marker
	// must not appear among the citable versions. An Issue source leaves this
	// Link unowned, so these writes do not advance a second subject's history.
	run(t, "link", "beads/issue", "beads/target", "--id", "links/related", "--resource-type", relatedType, "--actor", author, "--json")
	run(t, "update", "links/related", "--properties", `{"note":"changed"}`, "--actor", author, "--json")
	removedLink := graphMixedResult[graphstore.LinkDeleteResult](t, run(t, "unlink", "links/related", "--unconditional", "--actor", author, "--json"))

	t.Run("json-member-set", func(t *testing.T) {
		// One plane getting the contract right says nothing about the others:
		// the Issue plane reads native issue_versions through a mapping table
		// while Memory and Link read graph_preview_versions, so the projection
		// is asserted on all three.
		for _, subject := range []struct{ selector, kind string }{
			{"beads/issue", "issue"},
			{"beads/memory", "memory"},
			{"links/related", "link"},
		} {
			resource, kind, rows, members := graphVersionsListed(t, run(t, "versions", subject.selector, "--json"))
			if resource != subject.selector || kind != subject.kind {
				t.Fatalf("listing misidentified its subject: resource=%q kind=%q want %q/%q", resource, kind, subject.selector, subject.kind)
			}
			graphVersionsAssertKeys(t, subject.selector, members)
			for i, row := range rows {
				// A member can be present and useless. The token is the only
				// citable address on this plane, and change_at is emitted at
				// fixed-width microseconds rather than RFC3339Nano precisely so
				// rows stay column-comparable.
				if row.Version == "" {
					t.Fatalf("%s row %d has no citable token", subject.selector, i)
				}
				if len(row.ChangeAt) != len("2006-01-02T15:04:05.000000Z") || !strings.HasSuffix(row.ChangeAt, "Z") {
					t.Fatalf("%s row %d change_at is not fixed-width UTC microseconds: %q", subject.selector, i, row.ChangeAt)
				}
				if row.Actor != author {
					t.Fatalf("%s row %d lost its actor: %q", subject.selector, i, row.Actor)
				}
			}
		}
	})

	t.Run("removed-link-lists-only-citable-versions", func(t *testing.T) {
		_, kind, rows, _ := graphVersionsListed(t, run(t, "versions", "links/related", "--json"))
		if kind != "link" {
			t.Fatalf("unlinked Link listed as %q", kind)
		}
		if len(rows) != 2 {
			t.Fatalf("create/update should retain two citable Link versions, got %d: %+v", len(rows), rows)
		}
		if removedLink.Link.Version == "" {
			t.Fatal("unlink did not report its private deletion token")
		}
		human := run(t, "versions", "links/related")
		if strings.Contains(human, removedLink.Link.Version) {
			t.Fatalf("non-citable deletion token appeared in human version list:\n%s", human)
		}
		if !strings.Contains(human, "This Link has been removed.") {
			t.Fatalf("removed Link has no human state notice:\n%s", human)
		}
		for _, row := range rows {
			if row.Version == removedLink.Link.Version {
				t.Fatalf("non-citable deletion token appeared as a Resource version: %+v", row)
			}
			// Every returned token must actually work as an exact address.
			run(t, "show", "links/related", "--version", row.Version, "--json")
		}
		_, stderr, code := graphVersionsProcess(t, bd, work, home, "show", "links/related", "--version", removedLink.Link.Version, "--json")
		if code == 0 || !strings.Contains(stderr, "gone") {
			t.Fatalf("private deletion token unexpectedly citable: code=%d stderr=%q", code, stderr)
		}
		for _, selector := range []string{"beads/issue", "beads/memory"} {
			clean := run(t, "versions", selector)
			if strings.Contains(clean, "This Link has been removed.") {
				t.Fatalf("%s rendered Link removal guidance:\n%s", selector, clean)
			}
		}
	})

	t.Run("history-aliases-versions", func(t *testing.T) {
		// `bd history` means Dolt commits on the native plane; a graph
		// workspace exposes no Dolt-commit view, so there it is the same
		// answer as `bd versions`. "Same answer" has to mean byte-identical
		// output, not merely similar, or the alias is a second implementation.
		for _, selector := range []string{"beads/issue", "beads/memory", "links/related"} {
			for _, mode := range [][]string{{"--json"}, nil} {
				versions := run(t, append([]string{"versions", selector}, mode...)...)
				history := run(t, append([]string{"history", selector}, mode...)...)
				if versions != history {
					t.Fatalf("history is not an alias for versions on %s (%v):\nversions=%q\nhistory=%q", selector, mode, versions, history)
				}
			}
		}
		// --limit is a native history flag this plane does not implement.
		// Accepting and ignoring it would hand back a full listing that the
		// caller reads as the newest two entries, which is a wrong answer
		// dressed as a complete one.
		for _, args := range [][]string{
			{"history", "beads/issue", "--limit", "2"},
			{"history", "beads/issue", "--limit", "2", "--json"},
		} {
			out, stderr, code := graphVersionsProcess(t, bd, work, home, args...)
			if code != 5 || out != "" || !strings.Contains(stderr, "capability_unavailable") {
				t.Fatalf("bd %v did not refuse --limit: code=%d stdout=%q stderr=%q", args, code, out, stderr)
			}
			if !strings.Contains(stderr, "limit") {
				t.Fatalf("bd %v refused without naming the flag: %q", args, stderr)
			}
		}
	})

	t.Run("unallocated-path-is-one-not-found", func(t *testing.T) {
		// Regression guard. graphVersionsNotFound re-wraps graphstore.ErrNotFound
		// instead of calling graphFailure from inside the withGraphStore
		// closure. Calling graphFailure there printed not_found, returned an
		// exitError that matched no sentinel, and let graphStorageError print
		// graph_not_initialized over the top and exit 5: two diagnostics and
		// the wrong code for one failure.
		for _, args := range [][]string{
			{"versions", "beads/never-allocated"},
			{"versions", "beads/never-allocated", "--json"},
			// The alias must not reinterpret the failure either.
			{"history", "beads/never-allocated"},
			// Links resolve through the same selector path, so an unallocated
			// links/PATH is the same single not_found rather than a malformed
			// selector.
			{"versions", "links/never-allocated"},
		} {
			out, stderr, code := graphVersionsProcess(t, bd, work, home, args...)
			if code != 3 {
				t.Fatalf("bd %v exited %d, want 3 (not_found): stderr=%q", args, code, stderr)
			}
			if out != "" {
				t.Fatalf("bd %v emitted success output for a missing path: %q", args, out)
			}
			if strings.Contains(stderr, "graph_not_initialized") {
				t.Fatalf("bd %v reported the store as uninitialized for a missing path: %q", args, stderr)
			}
			if lines := strings.Count(strings.TrimRight(stderr, "\n"), "\n") + 1; lines != 1 {
				t.Fatalf("bd %v printed %d diagnostics, want exactly 1: %q", args, lines, stderr)
			}
			if !strings.Contains(stderr, "not_found") {
				t.Fatalf("bd %v did not report not_found: %q", args, stderr)
			}
		}
	})

	t.Run("attribution-is-per-plane", func(t *testing.T) {
		// Attribution is native to Issues and absent on the other planes. The
		// two are rendered differently on purpose: a dash reads as "this plane
		// does not record it", an empty column reads as lost data.
		for _, subject := range []struct{ selector, wantJSON, wantHuman string }{
			{"beads/issue", "claimed", "claimed"},
			{"beads/memory", "", "-"},
			{"links/related", "", "-"},
		} {
			_, _, rows, _ := graphVersionsListed(t, run(t, "versions", subject.selector, "--json"))
			for i, row := range rows {
				if row.Attribution != subject.wantJSON {
					t.Fatalf("%s row %d attribution=%q want %q", subject.selector, i, row.Attribution, subject.wantJSON)
				}
			}
			human := run(t, "versions", subject.selector)
			parsed := graphVersionsHumanRows(t, subject.selector, human)
			if len(parsed) != len(rows) {
				t.Fatalf("%s rendered %d rows for %d versions:\n%s", subject.selector, len(parsed), len(rows), human)
			}
			for i, columns := range parsed {
				if columns[3] != subject.wantHuman {
					t.Fatalf("%s row %d ATTRIB column=%q want %q:\n%s", subject.selector, i, columns[3], subject.wantHuman, human)
				}
			}
		}
	})

	t.Run("ordering-is-newest-first", func(t *testing.T) {
		for _, selector := range []string{"beads/issue", "beads/memory", "links/related"} {
			_, _, rows, _ := graphVersionsListed(t, run(t, "versions", selector, "--json"))
			if len(rows) < 2 {
				t.Fatalf("%s has %d versions; ordering needs at least two to be observable", selector, len(rows))
			}
			graphVersionsAssertOrder(t, selector, rows)
			// The human rendering must agree. It is a separate code path from
			// the projection, and a listing whose two renderings disagree about
			// order is worse than either being wrong alone.
			human := run(t, "versions", selector)
			if !strings.Contains(human, "  REV  WHEN") || strings.Contains(human, "  ORD  WHEN") {
				t.Fatalf("%s did not render the native-compatible REV heading:\n%s", selector, human)
			}
			parsed := graphVersionsHumanRows(t, selector, human)
			if len(parsed) != len(rows) {
				t.Fatalf("%s rendered %d rows for %d versions:\n%s", selector, len(parsed), len(rows), human)
			}
			for i, columns := range parsed {
				if columns[0] != strconv.FormatInt(rows[i].Ordinal, 10) {
					t.Fatalf("%s rendered ordinal %q at row %d, JSON says %d:\n%s", selector, columns[0], i, rows[i].Ordinal, human)
				}
			}
		}
	})
}
