package graphstore

import (
	"context"
	"database/sql"
	"fmt"

	txmemory "github.com/steveyegge/beads/internal/storage/memoryops"
)

// Continuity projections share the database, but have a separate acquisition
// budget from canonical graph records. These limits include keys and row costs;
// they do not measure Go heap, engine caches or retained Dolt history.
const (
	PreviewContinuityReadByteLimit = 64 << 20
	PreviewContinuityReadRowLimit  = 65536
)

type acquisitionUsage struct {
	resources, currentBytes, continuityRows, continuityBytes uint64
}

func continuityReadUsage(ctx context.Context, tx *sql.Tx) (rows, bytes uint64, err error) {
	prefix := txmemory.StorageKey("")
	err = tx.QueryRowContext(ctx, "SELECT COUNT(*), CAST(LEAST(COALESCE(SUM(256+OCTET_LENGTH(`key`)+COALESCE(OCTET_LENGTH(value),0)),0),?) AS UNSIGNED) FROM config WHERE BINARY LEFT(`key`, CHAR_LENGTH(?)) = BINARY ?", uint64(1)<<60, prefix, prefix).Scan(&rows, &bytes)
	return rows, bytes, err
}

func checkContinuityReadBounds(ctx context.Context, tx *sql.Tx) error {
	rows, bytes, err := continuityReadUsage(ctx, tx)
	if err != nil {
		return err
	}
	if rows > PreviewContinuityReadRowLimit || bytes > PreviewContinuityReadByteLimit {
		return fmt.Errorf("%w: continuity exceeds %d rows or %d acquisition bytes before payload decoding", ErrLimitExceeded, PreviewContinuityReadRowLimit, PreviewContinuityReadByteLimit)
	}
	return nil
}

func readAcquisitionUsage(ctx context.Context, tx *sql.Tx) (acquisitionUsage, error) {
	var usage acquisitionUsage
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM graph_preview_catalog WHERE allocation_state='live'").Scan(&usage.resources); err != nil {
		return usage, err
	}
	var err error
	usage.currentBytes, err = currentReadBytes(ctx, tx)
	if err != nil {
		return usage, err
	}
	usage.continuityRows, usage.continuityBytes, err = continuityReadUsage(ctx, tx)
	return usage, err
}

// A normal write must fit every read budget. Old or out-of-band oversized state
// may be reduced (or left unchanged by a no-op), but no dimension may grow until
// all budgets fit. The comparison is numeric and stays in the write transaction.
func checkAcquisitionGrowth(before, after acquisitionUsage) error {
	dimensions := [...]struct{ limit, before, after uint64 }{
		{PreviewSnapshotLimit, before.resources, after.resources},
		{PreviewCurrentReadByteLimit, before.currentBytes, after.currentBytes},
		{PreviewContinuityReadRowLimit, before.continuityRows, after.continuityRows},
		{PreviewContinuityReadByteLimit, before.continuityBytes, after.continuityBytes},
	}
	oversized := false
	for _, dimension := range dimensions {
		oversized = oversized || dimension.before > dimension.limit
	}
	for _, dimension := range dimensions {
		if (oversized && dimension.after > dimension.before) || (!oversized && dimension.after > dimension.limit) {
			return fmt.Errorf("%w: graph write would exceed or grow an existing acquisition limit", ErrLimitExceeded)
		}
	}
	return nil
}
