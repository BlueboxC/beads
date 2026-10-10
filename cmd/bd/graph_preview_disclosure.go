package main

import (
	"fmt"

	"github.com/steveyegge/beads/internal/storage/graphstore"
)

// This is a preview result projection, not a public History or wire contract.
// The store supplies the predecessor captured in the successful transaction.
func graphPreviewReplacementResult(result any, summary string, replaced *graphstore.ReplacedMemory, err error) (any, string, error) {
	if err != nil {
		return result, "", err
	}
	human, err := graphPreviewReplacementSummary(summary, replaced)
	return result, human, err
}

func graphPreviewReplacementSummary(summary string, replaced *graphstore.ReplacedMemory) (string, error) {
	if replaced == nil {
		return summary, nil
	}
	if replaced.Attribution.Actor == "" {
		if replaced.Attribution.Status != "" && replaced.Attribution.Status != "unknown" {
			return "", fmt.Errorf("%w: stored attribution has no actor", graphstore.ErrInvalidStore)
		}
		return fmt.Sprintf("%s\nReplaced Memory %s version %s; no recorded attribution", summary, replaced.ID, replaced.Version), nil
	}
	basis, err := graphPublicAttributionBasis(replaced.Attribution.Status)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s\nReplaced Memory %s version %s; recorded attribution: actor=%q basis=%q recordedAt=%q",
		summary, replaced.ID, replaced.Version, replaced.Attribution.Actor,
		basis, replaced.Attribution.RecordedAt), nil
}
