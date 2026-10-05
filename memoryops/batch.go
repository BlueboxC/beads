package memoryops

import "context"

// AtomicMemories is an optional capability for publishing or pruning derived
// generations. Guards and mutations share one transaction; a mismatch writes
// nothing. Implementations must skip equal values and roll back on any error.
// The basic Memories role remains compatible with existing callers.
type AtomicMemories interface {
	Apply(context.Context, BatchRequest) (BatchResult, error)
}

type BatchRequest struct {
	// An empty expected value means absent (Remember refuses empty values).
	Expected map[string]string
	Remember map[string]string
	Forget   []string
}

type BatchResult struct {
	Written int `json:"written"`
	Deleted int `json:"deleted"`
}
