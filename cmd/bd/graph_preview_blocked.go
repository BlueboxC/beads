package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage/graphstore"
)

const graphIssueBlockedOutputLimit = 16 << 20

func graphPreviewBlockedInput(cmd *cobra.Command, args []string) error {
	if err := graphPreviewFlags(cmd, "label", "label-any", "exclude-label"); err != nil {
		return err
	}
	if len(args) != 0 {
		return graphFailure("invalid_selector", "graph blocked takes no positional arguments", 2)
	}
	if err := graphPreviewLabelFilters(cmd); err != nil {
		return err
	}
	if raw := os.Getenv(maxRowsEnvVar); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			return graphFailure("invalid_properties", "BEADS_MAX_ROWS must be a non-negative integer", 2)
		}
		if value > 0 {
			return graphFailure("capability_unavailable", "graph blocked requires the complete view; unset BEADS_MAX_ROWS", 5)
		}
	}
	return nil
}

func runGraphPreviewBlocked(cmd *cobra.Command, args []string) error {
	if err := graphPreviewBlockedInput(cmd, args); err != nil {
		return err
	}
	filter := blockedFilterFromFlags(cmd)
	return withGraphStoreOutput(func(ctx context.Context, s *graphstore.Store) (any, string, error) {
		issues, err := s.BlockedIssuesFiltered(ctx, filter)
		if err != nil {
			return nil, "", err
		}
		output, err := renderGraphIssueBlocked(issues, jsonOutput, quietFlag)
		return nil, output, err
	}, func(_ any, output string) error { _, err := fmt.Fprint(cmd.OutOrStdout(), output); return err })
}

func renderGraphIssueBlocked(issues []graphstore.BlockedIssue, structured, quiet bool) (string, error) {
	if issues == nil {
		issues = []graphstore.BlockedIssue{}
	}
	var human strings.Builder
	if !structured && !quiet {
		fmt.Fprintf(&human, "Dependency-blocked Issues (%d; graph preview)\n", len(issues))
		for _, item := range issues {
			fmt.Fprintf(&human, "%q P%d %q\n", item.Issue.ID, item.Issue.Properties.Priority, item.Issue.Properties.Title)
			for _, blocker := range item.BlockedBy {
				fmt.Fprintf(&human, "  blocked by %q\n", blocker)
			}
		}
	}
	var output bytes.Buffer
	if err := graphPrintTo(&output, issues, strings.TrimSuffix(human.String(), "\n"), quiet, structured); err != nil {
		return "", err
	}
	if output.Len() > graphIssueBlockedOutputLimit {
		return "", fmt.Errorf("%w: graph blocked output exceeds %d bytes", graphstore.ErrLimitExceeded, graphIssueBlockedOutputLimit)
	}
	return output.String(), nil
}
