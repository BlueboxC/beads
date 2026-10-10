package main

import (
	"encoding/json"

	"github.com/spf13/cobra"
	graph "github.com/steveyegge/beads/graphops"
	"github.com/steveyegge/beads/internal/storage/issueops"
	publicops "github.com/steveyegge/beads/issueops"
)

func graphPreviewMetadataCreate(cmd *cobra.Command) (json.RawMessage, error) {
	patch, err := graphPreviewMetadataPatch(cmd)
	if err != nil {
		return nil, err
	}
	metadata, _, err := issueops.ApplyMetadataPatch(nil, patch)
	if err != nil {
		return nil, graphFailure("invalid_properties", err.Error(), 2)
	}
	return metadata, nil
}

// This is the ordinary update flag contract: a complete object merges keys,
// typed set values are applied next, and unset wins last. Storage resolves it
// against the checked predecessor inside the same write transaction.
func graphPreviewMetadataPatch(cmd *cobra.Command) (publicops.MetadataPatch, error) {
	var patch publicops.MetadataPatch
	merge := cmd.Flags().Changed("metadata")
	set := cmd.Flags().Changed("set-metadata")
	unset := cmd.Flags().Changed("unset-metadata")
	if merge && (set || unset) {
		return patch, graphFailure("invalid_properties", "cannot combine --metadata with --set-metadata or --unset-metadata", 2)
	}
	if merge {
		value, _ := cmd.Flags().GetString("metadata")
		parsed, err := readMetadataFlag(value)
		if err != nil {
			return patch, graphFailure("invalid_properties", err.Error(), 2)
		}
		canonical, err := graph.CanonicalizeJSON(parsed)
		if err != nil {
			return patch, graphFailure("invalid_properties", err.Error(), 2)
		}
		if len(canonical) == 0 || canonical[0] != '{' {
			return patch, graphFailure("invalid_properties", "metadata must be a JSON object", 2)
		}
		patch.Merge = publicops.Field[json.RawMessage]{Set: true, Value: parsed}
	}
	if set {
		flags, _ := cmd.Flags().GetStringArray("set-metadata")
		parsed, err := parseSetMetadataFlags(flags)
		if err != nil {
			return patch, graphFailure("invalid_properties", err.Error(), 2)
		}
		patch.Set = parsed
	}
	if unset {
		patch.Unset, _ = cmd.Flags().GetStringArray("unset-metadata")
	}
	return patch, nil
}

func graphPreviewMetadataFlagsChanged(cmd *cobra.Command) bool {
	return cmd.Flags().Changed("metadata") || cmd.Flags().Changed("set-metadata") || cmd.Flags().Changed("unset-metadata")
}
