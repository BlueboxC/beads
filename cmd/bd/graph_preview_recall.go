package main

import (
	"context"
	"fmt"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage/graphstore"
)

// Recall follows ordinary bd's human output: one trailing newline, even when
// the stored body is empty. Quiet does not suppress the requested body.
// The structured Memory contract requires fields this preview does not provide.
func runGraphPreviewRecall(cmd *cobra.Command, args []string) error {
	if err := graphPreviewFlags(cmd, "version"); err != nil {
		return err
	}
	if jsonOutput {
		return graphFailure("capability_unavailable", "graph recall --json requires the complete Memory representation; use show --json for the experimental record", 5)
	}
	if len(args) != 1 {
		return graphFailure("invalid_selector", "graph recall requires one canonical Memory selector", 2)
	}
	path, err := graphPreviewResourcePath(graphPreviewConfig.GraphScopeURL, args[0])
	if err != nil {
		return graphFailure("invalid_selector", err.Error(), 2)
	}
	version, _ := cmd.Flags().GetString("version")
	versioned := cmd.Flags().Changed("version")
	if versioned && (version == "" || !utf8.ValidString(version) || len(version) > graphstore.PreviewVersionTokenLimit) {
		return graphFailure("invalid_selector", "--version requires a nonempty UTF-8 token of at most 4096 bytes", 2)
	}
	return withGraphStoreOutput(func(ctx context.Context, s *graphstore.Store) (any, string, error) {
		var value any
		var err error
		if versioned {
			value, err = s.ReadVersion(ctx, path, version)
		} else {
			value, err = s.Read(ctx, path)
		}
		if err != nil {
			return nil, "", err
		}
		memory, ok := value.(graphstore.Record)
		if !ok {
			return nil, "", fmt.Errorf("%w: recall requires a Memory Bead", graphstore.ErrCapabilityUnavailable)
		}
		return nil, memory.Properties.Body, nil
	}, func(_ any, body string) error {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), body)
		return err
	})
}
