package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	graph "github.com/steveyegge/beads/graphops"
	"github.com/steveyegge/beads/internal/storage/graphstore"
)

func runGraphPreviewReopen(cmd *cobra.Command, args []string) error {
	if err := graphPreviewWritePolicy(); err != nil {
		return err
	}
	if err := graphPreviewFlags(cmd, "reason"); err != nil {
		return err
	}
	if len(args) == 0 {
		return graphFailure("invalid_selector", "graph reopen requires at least one Bead ID or beads/PATH", 2)
	}
	paths := make([]string, len(args))
	for i, selector := range args {
		path, err := graphPreviewResourcePath(graphPreviewConfig.GraphScopeURL, selector)
		if err != nil {
			return graphFailure("invalid_selector", err.Error(), 2)
		}
		if err := graph.ValidateBeadPath(path); err != nil {
			return graphFailure("invalid_selector", err.Error(), 2)
		}
		paths[i] = path
	}
	reason, _ := cmd.Flags().GetString("reason")
	var hadError bool
	err := withGraphStore(func(ctx context.Context, store *graphstore.Store) (any, string, error) {
		results := make([]graphstore.IssueMutationResult, 0, len(paths))
		var human strings.Builder
		for i, path := range paths {
			result, err := store.ReopenIssue(ctx, path, reason, getActorWithGit())
			if err != nil {
				if len(paths) == 1 {
					return nil, "", err
				}
				_ = graphStorageError(fmt.Errorf("reopening %s: %w", args[i], err))
				hadError = true
				continue
			}
			if !result.Changed {
				if len(paths) == 1 {
					return result, fmt.Sprintf("Unchanged %s (status %q)\n", result.Issue.ID, result.Issue.Properties.Status), nil
				}
				fmt.Fprintf(os.Stderr, "%s is not closed (status: %s); nothing to do\n", args[i], result.Issue.Properties.Status)
				continue
			}
			results = append(results, result)
			fmt.Fprintf(&human, "Reopened %s\n", result.Issue.ID)
		}
		if len(paths) == 1 {
			return results[0], human.String(), nil
		}
		return results, strings.TrimSuffix(human.String(), "\n"), nil
	})
	if err != nil {
		return err
	}
	if hadError {
		return SilentExit()
	}
	return nil
}
