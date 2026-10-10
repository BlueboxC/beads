package versioncontrolops

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Compact squashes old Dolt commits into a single base commit while preserving
// recent commits via cherry-pick. The recipe:
//  1. Create temp branch at the boundary commit (last old commit)
//  2. Checkout temp branch
//  3. Soft-reset to initial commit (collapses old history into working set)
//  4. Commit as single squashed base, dated at the boundary
//  5. Cherry-pick each recent commit with its original committer and date
//  6. Checkout main, hard-reset to temp branch
//  7. Delete temp branch
//
// Callers should run PruneRemoteRefs and then DoltGC afterward to reclaim disk
// space — remote-tracking refs still anchor the pre-compact chain, and GC
// alone reclaims nothing while they exist (bd-agctw).
//
// conn must be a single database connection (not a pooled *sql.DB) since the
// stored procedures rely on session-scoped state (current branch, working set).
func Compact(ctx context.Context, conn DBConn, initialHash, boundaryHash string, oldCommits int, recentHashes []string) (retErr error) {
	// Capture the timeline before rewriting it (#7294). Cherry-pick preserves
	// authorship itself; committer overrides avoid amend overwriting the author.
	meta, err := compactCommitMetadata(ctx, conn)
	if err != nil {
		return fmt.Errorf("compact step %q: %w", "read commit metadata", err)
	}
	boundary, ok := meta[boundaryHash]
	if !ok {
		return fmt.Errorf("compact: boundary commit %s not found in dolt_log", boundaryHash)
	}
	for _, hash := range recentHashes {
		if _, ok := meta[hash]; !ok {
			return fmt.Errorf("compact: recent commit %s not found in dolt_log", hash)
		}
	}
	var identity compactCommit
	if err := conn.QueryRowContext(ctx, "SELECT @@dolt_committer_name, @@dolt_committer_email, @@dolt_committer_date").Scan(&identity.name, &identity.email, &identity.date); err != nil {
		return fmt.Errorf("compact: read session committer: %w", err)
	}
	setIdentity := func(ctx context.Context, m compactCommit) error {
		_, err := conn.ExecContext(ctx, "SET @@dolt_committer_name = ?, @@dolt_committer_email = ?, @@dolt_committer_date = ?", m.name, m.email, m.date)
		return err
	}
	// Restore the caller's overrides, including a nonempty date, on every exit.
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := setIdentity(cleanupCtx, identity); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("compact: restore session committer: %w", err))
		}
	}()
	branchCreated := false

	// Best-effort cleanup: if any step fails after creating the temp branch,
	// try to return to main and delete the temp branch so future compactions
	// aren't blocked by a leftover branch.
	defer func() {
		if retErr != nil && branchCreated {
			_, _ = conn.ExecContext(ctx, "CALL DOLT_CHECKOUT('main')")
			_, _ = conn.ExecContext(ctx, "CALL DOLT_BRANCH('-D', 'compact-tmp')")
		}
	}()

	execSQL := func(name, query string, args ...interface{}) error {
		if _, err := conn.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("compact step %q: %w", name, err)
		}
		return nil
	}

	if err := execSQL("create temp branch", "CALL DOLT_BRANCH('compact-tmp', ?)", boundaryHash); err != nil {
		return err
	}
	branchCreated = true

	if err := execSQL("checkout temp", "CALL DOLT_CHECKOUT('compact-tmp')"); err != nil {
		return err
	}
	if err := execSQL("soft reset to initial", "CALL DOLT_RESET('--soft', ?)", initialHash); err != nil {
		return err
	}
	msg := fmt.Sprintf("compact: squash %d commits into base snapshot", oldCommits)
	baseIdentity := identity
	baseIdentity.date = boundary.date
	if err := setIdentity(ctx, baseIdentity); err != nil {
		return fmt.Errorf("compact: date squashed base: %w", err)
	}
	if err := execSQL("commit squashed base", "CALL DOLT_COMMIT('-Am', ?, '--date', ?)", msg, boundary.date); err != nil {
		return err
	}

	// --allow-empty: the preserved window can contain empty commits (a Dolt
	// auto-commit with no table change, or a bd create double-commit whose
	// leading member has 0 changed tables). Without this flag DOLT_CHERRY_PICK
	// aborts the entire replay at the first empty commit with Error 1105
	// ("The previous cherry-pick commit is empty. Use --allow-empty ..."),
	// leaving compaction permanently blocked on active databases. See #3815.
	for _, hash := range recentHashes {
		if err := setIdentity(ctx, meta[hash]); err != nil {
			return fmt.Errorf("compact: restore committer for %s: %w", hash, err)
		}
		if err := execSQL(fmt.Sprintf("cherry-pick %s", hash[:min(8, len(hash))]), "CALL DOLT_CHERRY_PICK('--allow-empty', ?)", hash); err != nil {
			return err
		}
	}

	if err := execSQL("checkout main", "CALL DOLT_CHECKOUT('main')"); err != nil {
		return err
	}
	if err := execSQL("reset main to compacted", "CALL DOLT_RESET('--hard', 'compact-tmp')"); err != nil {
		return err
	}
	if err := execSQL("delete temp branch", "CALL DOLT_BRANCH('-D', 'compact-tmp')"); err != nil {
		return err
	}

	return nil
}

// Adapted from uschtwill's #7296 metadata snapshot and bee-ghosttrack's
// committer-variable review. Formatting in SQL retains UTC millisecond dates
// across both driver paths without imposing the connection's local time zone.
type compactCommit struct{ name, email, date string }

func compactCommitMetadata(ctx context.Context, conn DBConn) (map[string]compactCommit, error) {
	rows, err := conn.QueryContext(ctx, "SELECT commit_hash, committer, email, DATE_FORMAT(date, '%Y-%m-%dT%H:%i:%s.%fZ') FROM dolt_log")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	meta := make(map[string]compactCommit)
	for rows.Next() {
		var hash string
		var m compactCommit
		if err := rows.Scan(&hash, &m.name, &m.email, &m.date); err != nil {
			return nil, err
		}
		meta[hash] = m
	}
	return meta, rows.Err()
}
