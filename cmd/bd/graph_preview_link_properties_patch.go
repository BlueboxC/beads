package main

import (
	"context"
	"fmt"
	"unicode/utf8"

	"github.com/spf13/cobra"
	graph "github.com/steveyegge/beads/graphops"
	"github.com/steveyegge/beads/internal/storage/graphstore"
)

// Resource selection and write policy precede this route. Source ownership is
// checked in the writer; an Issue source does not require an owned-source guard.
func runGraphPreviewLinkPropertiesPatch(cmd *cobra.Command, path string) error {
	request, err := graphPreviewLinkPropertiesPatchRequest(cmd, path)
	if err != nil {
		return err
	}
	return withGraphStore(func(ctx context.Context, store *graphstore.Store) (any, string, error) {
		result, err := store.PatchLinkProperties(ctx, request)
		if err != nil {
			return nil, "", err
		}
		verb := "Updated"
		if !result.Changed {
			verb = "Unchanged"
		}
		return graphPreviewReplacementResult(result, fmt.Sprintf("%s %s", verb, result.Link.ID), result.ReplacedSource, nil)
	})
}

func graphPreviewLinkPropertiesPatchRequest(cmd *cobra.Command, path string) (graphstore.LinkPropertiesPatchRequest, error) {
	var request graphstore.LinkPropertiesPatchRequest
	if err := graphPreviewFlags(cmd, "patch", "metadata", "set-metadata", "unset-metadata", "if-revision", "if-source-revision", "unconditional-source"); err != nil {
		return request, err
	}
	if err := graph.ValidateLinkPath(path); err != nil {
		return request, graphFailure("invalid_selector", err.Error(), 2)
	}
	if !cmd.Flags().Changed("patch") {
		return request, graphFailure("invalid_properties", "Link properties patch requires --patch JSON, @file, or @-", 2)
	}
	revision, unconditional, err := graphPreviewEditRevisionGuard(cmd)
	if err != nil {
		return request, err
	}
	sourceRevision, unconditionalSource, err := graphPreviewRevisionGuard(cmd, true, false)
	if err != nil {
		return request, err
	}
	for _, token := range []struct{ name, value string }{{"if-revision", revision}, {"if-source-revision", sourceRevision}} {
		if !utf8.ValidString(token.value) || len(token.value) > graphstore.PreviewVersionTokenLimit {
			return request, graphFailure("invalid_selector", fmt.Sprintf("--%s requires a UTF-8 token of at most %d bytes", token.name, graphstore.PreviewVersionTokenLimit), 2)
		}
	}
	input, _ := cmd.Flags().GetString("patch")
	raw, err := graphPreviewMemoryPropertiesPatchInput(input, cmd.InOrStdin())
	if err != nil {
		return request, graphFailure("invalid_properties", err.Error(), 2)
	}
	metadata, err := graphPreviewMetadataPatch(cmd)
	if err != nil {
		return request, err
	}
	return graphstore.LinkPropertiesPatchRequest{Path: path, Patch: raw, Actor: getActorWithGit(), ExpectedRevision: revision, Unconditional: unconditional, ExpectedSourceRevision: sourceRevision, UnconditionalSource: unconditionalSource, Metadata: metadata}, nil
}
