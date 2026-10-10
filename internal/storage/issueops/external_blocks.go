package issueops

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	publicops "github.com/steveyegge/beads/issueops"
)

// ExternalBlockersInTx evaluates stored external blocks and their parent-child
// descendants without persisting provider state into the local is_blocked cache.
func ExternalBlockersInTx(ctx context.Context, tx DBTX) (map[string][]string, error) {
	blockers := make(map[string][]string)
	type edge struct{ issue, target string }
	var edges []edge
	refs := make(map[string]bool)
	for _, tables := range []sqlbuild.FilterTables{IssuesFilterTables, WispsFilterTables} {
		//nolint:gosec // G201: table names are hardcoded; all values are literals.
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(`SELECT d.issue_id, d.depends_on_external
   FROM %s d JOIN %s s ON s.id = d.issue_id
   WHERE d.type = 'blocks' AND d.depends_on_external IS NOT NULL
    AND s.status NOT IN ('closed', 'pinned')`, tables.Dependencies, tables.Main))
		if err != nil {
			if tables.Main == "wisps" && missingOptionalWispTable(err) {
				continue
			}
			return nil, fmt.Errorf("read external prerequisites: %w", err)
		}
		for rows.Next() {
			var e edge
			if err := rows.Scan(&e.issue, &e.target); err != nil {
				_ = rows.Close()
				return nil, err
			}
			edges = append(edges, e)
			refs[e.target] = true
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return nil, err
		}
	}
	if len(edges) == 0 {
		return blockers, nil
	}
	var satisfied map[string]bool
	if resolve := publicops.ExternalResolverFromContext(ctx); resolve != nil {
		targets := make([]string, 0, len(refs))
		for ref := range refs {
			targets = append(targets, ref)
		}
		sort.Strings(targets)
		lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		var err error
		satisfied, err = resolve(lookupCtx, targets)
		if err == nil {
			err = lookupCtx.Err()
		}
		cancel()
		if err != nil {
			return nil, fmt.Errorf("resolve external prerequisites: %w", err)
		}
	}
	for _, e := range edges {
		if !satisfied[e.target] {
			blockers[e.issue] = append(blockers[e.issue], e.target)
		}
	}
	if len(blockers) == 0 {
		return blockers, nil
	}
	// Read active child edges once, then walk a visited set. This terminates even
	// for an imported parent cycle and handles children crossing issue/wisp planes.
	children := make(map[string][]string)
	for _, tables := range []sqlbuild.FilterTables{IssuesFilterTables, WispsFilterTables} {
		//nolint:gosec // G201: table names and target expression are hardcoded.
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(`SELECT d.issue_id, COALESCE(d.depends_on_issue_id, d.depends_on_wisp_id)
   FROM %s d JOIN %s s ON s.id = d.issue_id
   WHERE d.type = 'parent-child' AND s.status NOT IN ('closed', 'pinned')
    AND COALESCE(d.depends_on_issue_id, d.depends_on_wisp_id) IS NOT NULL`, tables.Dependencies, tables.Main))
		if err != nil {
			if tables.Main == "wisps" && missingOptionalWispTable(err) {
				continue
			}
			return nil, fmt.Errorf("read external blocker descendants: %w", err)
		}
		for rows.Next() {
			var child, parent string
			if err := rows.Scan(&child, &parent); err != nil {
				_ = rows.Close()
				return nil, err
			}
			children[parent] = append(children[parent], child)
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return nil, err
		}
	}
	queue := make([]string, 0, len(blockers))
	for id := range blockers {
		queue = append(queue, id)
	}
	sort.Strings(queue)
	for i := 0; i < len(queue); i++ {
		for _, child := range children[queue[i]] {
			if _, seen := blockers[child]; seen {
				continue
			}
			blockers[child] = []string{queue[i]}
			queue = append(queue, child)
		}
	}
	for id, refs := range blockers {
		sort.Strings(refs)
		blockers[id] = slices.Compact(refs)
	}
	return blockers, nil
}

// ExternalBlockedIDsInTx returns a stable exclusion set for pre-limit queries.
func ExternalBlockedIDsInTx(ctx context.Context, tx DBTX) ([]string, error) {
	blockers, err := ExternalBlockersInTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(blockers))
	for id := range blockers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}
