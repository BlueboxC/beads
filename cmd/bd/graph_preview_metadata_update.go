package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage/graphstore"
)

// The read chooses a writer only. It supplies no guard or update base: each
// selected writer re-reads and checks the current Resource in its transaction.
func runGraphPreviewUpdateMetadataOnly(cmd *cobra.Command, path string) error {
	if err := graphPreviewFlags(cmd, "metadata", "set-metadata", "unset-metadata", "if-revision", "if-source-revision", "unconditional-source"); err != nil {
		return err
	}
	patch, err := graphPreviewMetadataPatch(cmd)
	if err != nil {
		return err
	}
	revision, unconditional, err := graphPreviewEditRevisionGuard(cmd)
	if err != nil {
		return err
	}
	sourceRevision, unconditionalSource, err := graphPreviewRevisionGuard(cmd, true, false)
	if err != nil {
		return err
	}
	actor := getActorWithGit()
	return withGraphStore(func(ctx context.Context, store *graphstore.Store) (any, string, error) {
		current, err := store.Read(ctx, path)
		if err != nil {
			return nil, "", err
		}
		switch current.(type) {
		case graphstore.IssueRecord:
			if cmd.Flags().Changed("if-source-revision") || cmd.Flags().Changed("unconditional-source") {
				return nil, "", graphFailure("invalid_selector", "Issue updates have no owning-source guard", 2)
			}
			result, err := store.UpdateIssue(ctx, graphstore.UpdateIssueRequest{Path: path, Actor: actor, ExpectedRevision: revision, Unconditional: unconditional, Metadata: patch})
			return result, fmt.Sprintf("Updated %s", result.Issue.ID), err
		case graphstore.Record:
			if cmd.Flags().Changed("if-source-revision") || cmd.Flags().Changed("unconditional-source") {
				return nil, "", graphFailure("invalid_selector", "Memory updates have no owning-source guard", 2)
			}
			result, err := store.PatchMemory(ctx, graphstore.MemoryPatchRequest{Path: path, Actor: actor, ExpectedRevision: revision, Unconditional: unconditional, Metadata: patch})
			return graphPreviewReplacementResult(result, fmt.Sprintf("Updated %s", result.Memory.ID), result.Replaced, err)
		case graphstore.LinkRecord:
			result, err := store.UpdateLink(ctx, graphstore.LinkUpdateRequest{Path: path, Actor: actor, ExpectedRevision: revision, Unconditional: unconditional, ExpectedSourceRevision: sourceRevision, UnconditionalSource: unconditionalSource, Metadata: patch, MetadataOnly: true})
			return graphPreviewReplacementResult(result, fmt.Sprintf("Updated %s", result.Link.ID), result.ReplacedSource, err)
		default:
			return nil, "", graphFailure("capability_unavailable", "metadata update requires an admitted Bead or informational Link", 5)
		}
	})
}
