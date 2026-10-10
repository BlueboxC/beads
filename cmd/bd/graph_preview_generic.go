package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	graph "github.com/steveyegge/beads/graphops"
	"github.com/steveyegge/beads/internal/storage/graphstore"
)

var graphGenericFlags = []string{"view", "direction", "depth", "max-nodes", "max-links"}

func init() {
	graphCmd.Flags().String("view", "", "Experimental graph view: generic requires a graph workspace")
	graphCmd.Flags().String("direction", "both", "Generic Link direction: in, out or both (graph_mode link workspaces only)")
	graphCmd.Flags().Int("depth", 1, "Generic expansion depth (0..1000) (graph_mode link workspaces only)")
	graphCmd.Flags().Int("max-nodes", 100, "Generic emitted Bead bound (1..1000); exhaustion refuses (graph_mode link workspaces only)")
	graphCmd.Flags().Int("max-links", 200, "Generic emitted Link bound (1..1000); exhaustion refuses (graph_mode link workspaces only)")
}

func graphGenericFlagsChanged(cmd *cobra.Command) bool {
	for _, name := range graphGenericFlags {
		if cmd.Flags().Changed(name) {
			return true
		}
	}
	return false
}

func graphPreviewGenericInput(cmd *cobra.Command, args []string, scope string) (graphGenericInput, error) {
	zero := graphGenericInput{}
	if err := graphPreviewFlags(cmd, graphGenericFlags...); err != nil {
		return zero, err
	}
	view, _ := cmd.Flags().GetString("view")
	if !cmd.Flags().Changed("view") || view != "generic" {
		return zero, graphFailure("capability_unavailable", "graph preview requires explicit --view generic; native graph rendering is unavailable in a graph workspace", 5)
	}
	if len(args) != 1 {
		return zero, graphFailure("invalid_selector", "generic traversal requires one local Bead ID or beads/PATH", 2)
	}
	path, err := graphPreviewResourcePath(scope, args[0])
	if err != nil {
		return zero, graphFailure("invalid_selector", err.Error(), 2)
	}
	if err := graph.ValidateBeadPath(path); err != nil {
		return zero, graphFailure("invalid_selector", err.Error(), 2)
	}
	in := graphGenericInput{Root: graph.CanonicalURL(scope, path)}
	in.Direction, _ = cmd.Flags().GetString("direction")
	in.Depth, _ = cmd.Flags().GetInt("depth")
	in.MaxNodes, _ = cmd.Flags().GetInt("max-nodes")
	in.MaxLinks, _ = cmd.Flags().GetInt("max-links")
	if err := validateGraphGenericBounds(in); err != nil {
		return zero, graphFailure("invalid_selector", err.Error(), 2)
	}
	if raw := os.Getenv(maxRowsEnvVar); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			return zero, graphFailure("invalid_properties", "BEADS_MAX_ROWS must be a non-negative integer", 2)
		}
		if value > 0 {
			return zero, graphFailure("capability_unavailable", "generic traversal uses explicit node/Link bounds; unset BEADS_MAX_ROWS", 5)
		}
	}
	return in, nil
}

func runGraphPreviewGeneric(cmd *cobra.Command, args []string) error {
	in, err := graphPreviewGenericInput(cmd, args, graphPreviewConfig.GraphScopeURL)
	if err != nil {
		return err
	}
	return withGraphStoreOutput(func(ctx context.Context, s *graphstore.Store) (any, string, error) {
		snapshot, err := s.CurrentSnapshot(ctx)
		if err != nil {
			return nil, "", err
		}
		result, err := projectGraphGeneric(snapshot, s.ScopeURL(), in)
		if err != nil {
			return nil, "", err
		}
		output, err := renderGraphGeneric(result, jsonOutput, quietFlag)
		return nil, output, err
	}, func(_ any, output string) error { return writeGraphGeneric(cmd.OutOrStdout(), output) })
}

func writeGraphGeneric(out io.Writer, output string) error {
	n, err := io.WriteString(out, output)
	if err == nil && n != len(output) {
		return io.ErrShortWrite
	}
	return err
}

func renderGraphGeneric(result graphGenericResult, structured, quiet bool) (string, error) {
	var human strings.Builder
	if !structured && !quiet {
		fmt.Fprintf(&human, "Generic graph (summary; graph preview)\nRoot %q direction %s depth %d\n", result.Root, result.Direction, result.Depth)
		for _, node := range result.Nodes {
			fmt.Fprintf(&human, "Bead %q %q\n", node.ID, node.Title)
		}
		for _, link := range result.Links {
			fmt.Fprintf(&human, "Link %q %q -> %q\n", link.ID, link.Source, link.Target)
		}
		fmt.Fprintf(&human, "Complete: %t; frontier: %d\n", result.Complete, len(result.Frontier))
		for _, id := range result.Frontier {
			fmt.Fprintf(&human, "Frontier %q\n", id)
		}
	}
	var output bytes.Buffer
	if err := graphPrintTo(&output, result, strings.TrimSuffix(human.String(), "\n"), quiet, structured); err != nil {
		return "", err
	}
	if output.Len() > graphGenericOutputLimit {
		return "", fmt.Errorf("%w: generic summary output exceeds %d bytes", graphstore.ErrLimitExceeded, graphGenericOutputLimit)
	}
	return output.String(), nil
}
