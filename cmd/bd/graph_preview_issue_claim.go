package main

import (
	"context"
	"fmt"
	"unicode/utf8"

	"github.com/spf13/cobra"
	graph "github.com/steveyegge/beads/graphops"
	"github.com/steveyegge/beads/internal/storage/graphstore"
	"github.com/steveyegge/beads/internal/types"
)

// Claim is its existing atomic ownership transition, not a scalar edit or a
// graph revision guard. This preview admits the standalone form only.
func graphPreviewIssueClaimInput(cmd *cobra.Command, path, actor string) error {
	claim, err := cmd.Flags().GetBool("claim")
	if err != nil || !cmd.Flags().Changed("claim") || !claim {
		return graphFailure("invalid_properties", "graph Issue claim requires --claim=true", 2)
	}
	if err := graphPreviewFlags(cmd, "claim"); err != nil {
		return err
	}
	if err := graph.ValidateBeadPath(path); err != nil {
		return graphFailure("invalid_selector", err.Error(), 2)
	}
	if actor == "" || !utf8.ValidString(actor) {
		return graphFailure("invalid_properties", "Issue claim requires a nonempty UTF-8 actor", 2)
	}
	if err := types.CheckFieldLen("actor", actor); err != nil {
		return graphFailure("invalid_properties", err.Error(), 2)
	}
	return nil
}

func runGraphPreviewClaimIssue(cmd *cobra.Command, path string) error {
	actor := getActorWithGit()
	if err := graphPreviewIssueClaimInput(cmd, path, actor); err != nil {
		return err
	}
	err := withGraphStore(func(ctx context.Context, store *graphstore.Store) (any, string, error) {
		result, err := store.ClaimIssue(ctx, path, actor)
		if err != nil {
			return nil, "", err
		}
		verb := "Claimed"
		if !result.Changed {
			verb = "Unchanged"
		}
		return result, fmt.Sprintf("%s %s", verb, result.Issue.ID), nil
	})
	if err == nil {
		SetLastTouchedID(path)
	}
	return err
}
