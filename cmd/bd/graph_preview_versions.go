package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/storage/graphstore"
)

// graphVersionsNotFound is the "there is no such thing" shape, kept apart from
// the other two so a mistyped id never reads as a Resource with no history.
//
// It RE-WRAPS the sentinel rather than calling graphFailure, and that matters.
// graphFailure both prints a diagnostic and returns an exitError, so calling it
// from inside the withGraphStore closure emitted the message and then handed the
// exitError on to graphStorageError, which matched no sentinel, printed
// graph_not_initialized over the top and exited 5 instead of 3. Two diagnostics
// and the wrong code, for one failure. Wrapping keeps a single owner of the
// mapping: graphStorageError turns ErrNotFound into not_found, exit 3, once.
func graphVersionsNotFound(selector string) error {
	return fmt.Errorf("%w: nothing is allocated at %s in this workspace, so it has no version history"+
		" (a deleted Resource still lists its history, so this path was never allocated here)",
		graphstore.ErrNotFound, selector)
}

// graphVersionRowsJSON fixes the wire shape in the CLI layer, where the
// documented contract lives, instead of depending on struct tags in the store
// package. The names are deliberate and documented in
// docs/reference/graph-preview.md.
//
// `local_revision` names the same per-Resource ordering number as ordinary
// `bd versions --json`. It is NOT `revision`: graph records use that name for
// an opaque token, and native Issues use it for a row-lock CAS token. Neither
// token can be ordered or replaced by this store-local number.
func graphVersionRowsJSON(rows []graphstore.VersionRow) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, map[string]any{
			"local_revision": r.Ordinal,
			"version":        r.Version,
			// Fixed-width microseconds rather than RFC3339Nano, which strips
			// trailing zeros and so varies in width between rows
			// ("…51.65588Z" beside "…51.879092Z"). Parsers do not care, but
			// fixed-width columns and human comparison do, and this matches
			// the microsecond precision the human column shows.
			"change_at":   r.ChangeAt.UTC().Format("2006-01-02T15:04:05.000000Z07:00"),
			"actor":       r.Actor,
			"attribution": r.Attribution,
		})
	}
	return out
}

// runGraphPreviewVersions lists one graph Resource's versions, newest first.
//
// #5898 asks that a history answer never collapse distinct situations into one.
// On THIS plane only two of its three shapes are reachable, and saying so is
// part of the contract rather than a shortcut:
//
//   - here it is: an ordered list, newest first;
//   - there is no such thing: nothing is allocated at that path.
//
// The third shape, "something prevented a complete answer", is NOT reachable as
// a normal answer here. Every plane in a SchemaVersion-7 workspace can order,
// so a store that cannot answer is a corrupt store, and corruption is a refusal
// rather than an outcome. There is deliberately no "exists but empty" success
// case either: a subject's creation IS version 1, written in the same
// transaction as its catalog row, so an allocated subject with no versions
// means its creation version was lost. The store returns ErrInvalidStore for
// that and this command does not dress it up as an empty history.
//
// An empty list must never stand in for a refusal. "No versions" is a claim
// about the Resource; "this store cannot answer" is a claim about the store,
// and printing the first when the second is true is a wrong answer rather than
// an empty one.
func runGraphPreviewVersions(cmd *cobra.Command, args []string) error {
	if err := graphPreviewFlags(cmd); err != nil {
		return err
	}
	// Resolved with graphPreviewResourcePath rather than
	// graphPreviewBeadSelector: Links have versions too, so this accepts
	// links/PATH as well as beads/PATH. `compare` resolves the same way for
	// the same reason.
	path, err := graphPreviewResourcePath(graphPreviewConfig.GraphScopeURL, args[0])
	if err != nil {
		return graphFailure("invalid_selector", err.Error(), 2)
	}
	return withGraphStore(func(ctx context.Context, store *graphstore.Store) (any, string, error) {
		kind, rows, err := store.Versions(ctx, path)
		switch {
		case errors.Is(err, graphstore.ErrNotFound):
			// graphStorageError already maps ErrNotFound to not_found, so this
			// branch exists only to say something more useful than the store's
			// own wording. The distinction worth stating is that absence here
			// means NEVER ALLOCATED: a deleted Resource still lists its
			// history, so "no versions" and "no such path" are different
			// answers and a reader should not have to guess which one it got.
			return nil, "", graphVersionsNotFound(args[0])
		case err != nil:
			// Everything else is left whole for graphStorageError, which owns
			// the sentinel-to-code mapping. Interpreting it twice would print
			// two diagnostics for one failure.
			return nil, "", err
		}
		// The store retains a private deletion marker for a removed Link so
		// identity and historical state survive. It is not a Resource version:
		// exact reads refuse its token. List only citable states and keep the
		// removal notice outside the version rows.
		citable := make([]graphstore.VersionRow, 0, len(rows))
		removed := false
		for _, row := range rows {
			if row.Removed {
				if kind != graphstore.KindLink || removed {
					return nil, "", fmt.Errorf("%w: invalid deletion marker in version history", graphstore.ErrInvalidStore)
				}
				removed = true
				continue
			}
			citable = append(citable, row)
		}
		if len(citable) == 0 {
			return nil, "", fmt.Errorf("%w: allocated Resource has no citable versions", graphstore.ErrInvalidStore)
		}
		result := map[string]any{
			"resource": path,
			"kind":     string(kind),
			"versions": graphVersionRowsJSON(citable),
		}
		return result, renderGraphVersions(args[0], kind, citable, removed), nil
	})
}

// renderGraphVersions prints the token on its own line under each row.
//
// That is not a layout accident. On this plane the opaque version token is the
// ONLY citable address -- it is what `show --version` and `compare --from/--to`
// accept -- so a listing that elides it cannot be used for the thing the
// listing is for. The ordinal is 1-2 characters and the token is 32, which no
// single terminal row holds alongside a timestamp and an actor, so the token
// gets its own line rather than being truncated into uselessness.
func renderGraphVersions(selector string, kind graphstore.ResourceKind, rows []graphstore.VersionRow, removed bool) string {
	if len(rows) == 0 {
		// Defensive, and deliberately NOT reassuring. An allocated subject
		// always has at least version 1, because its creation version is
		// written in the same transaction as its catalog row, so the store
		// refuses zero rows as corruption before reaching here. If this ever
		// prints, something returned a shape that should not exist, and
		// claiming "nothing has changed since it was created" would be a
		// comforting lie about a broken store.
		return fmt.Sprintf(
			"No versions returned for %s, which should not be possible.\n\n"+
				"An allocated %s always has at least one version, because creation\n"+
				"records version 1. Treat this as a bug rather than an empty history.",
			selector, kind)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Versions of %s (%d)\n\n", selector, len(rows))
	fmt.Fprintf(&b, "  REV  WHEN                        WHO                  ATTRIB\n")
	for _, r := range rows {
		attrib := r.Attribution
		if attrib == "" {
			// Memory and Link carry no native attribution status. A dash
			// reads as "this plane does not record it"; an empty column reads
			// as missing data.
			attrib = "-"
		}
		fmt.Fprintf(&b, "  %-4d %-27s %-20.20s %s\n",
			r.Ordinal,
			// Microseconds are shown because they are the reason migration
			// 0069 widened change_at: two writes inside one second are
			// ordinary, and a whole-second rendering makes them look
			// simultaneous when they are not.
			r.ChangeAt.UTC().Format("2006-01-02 15:04:05.000000"),
			r.Actor,
			attrib)
		fmt.Fprintf(&b, "       %s\n", r.Version)
	}

	b.WriteString("\nREV is local to this store and is NOT a citable address: two\n")
	b.WriteString("clones can hold a different local revision for the same state. Cite the token\n")
	b.WriteString("under each row instead:\n\n")
	fmt.Fprintf(&b, "  bd show %s --version <token>\n", selector)
	fmt.Fprintf(&b, "  bd compare %s --from <token> --to <token>\n", selector)
	if removed {
		b.WriteString("\nThis Link has been removed. Its prior versions remain citable; deletion\n")
		b.WriteString("did not create a Link version.\n")
	}
	return b.String()
}
