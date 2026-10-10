package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage/graphstore"
)

func runGraphPreviewUpdate(cmd *cobra.Command, args []string) error {
	if err := graphPreviewWritePolicy(); err != nil {
		return err
	}
	if len(args) == 0 {
		if cmd.Flags().Changed("properties") || cmd.Flags().Changed("patch") {
			return graphFailure("invalid_selector", "Memory and Link property updates require an explicit Resource ID", 2)
		}
		lastTouched := GetLastTouchedID()
		if lastTouched == "" {
			return graphFailure("invalid_selector", "no Issue ID provided and no last touched Issue", 2)
		}
		args = []string{lastTouched}
	}
	if len(args) != 1 {
		return graphFailure("invalid_selector", "graph update requires one Bead ID (or beads/PATH) or explicit links/PATH", 2)
	}
	path, err := graphPreviewResourcePath(graphPreviewConfig.GraphScopeURL, args[0])
	if err != nil {
		return graphFailure("invalid_selector", err.Error(), 2)
	}
	if cmd.Flags().Changed("patch") {
		if strings.HasPrefix(path, "links/") {
			return runGraphPreviewLinkPropertiesPatch(cmd, path)
		}
		return runGraphPreviewMemoryPropertiesPatch(cmd, path)
	}
	if cmd.Flags().Changed("claim") {
		return runGraphPreviewClaimIssue(cmd, path)
	}
	if graphPreviewMetadataFlagsChanged(cmd) && !cmd.Flags().Changed("properties") && !graphPreviewIssueEditFlagsChanged(cmd) {
		return runGraphPreviewUpdateMetadataOnly(cmd, path)
	}
	if strings.HasPrefix(path, "links/") {
		return runGraphPreviewUpdateLink(cmd, args)
	}
	if graphPreviewIssueEditFlagsChanged(cmd) {
		return runGraphPreviewUpdateIssue(cmd, path)
	}
	return runGraphPreviewUpdateMemory(cmd, path)
}

// A top-level properties object edits only named Memory members. Missing
// members preserve their values in the checked storage transaction.
func graphPreviewMemoryProperties(properties map[string]any) (*string, *string, error) {
	var title, body *string
	for name, value := range properties {
		text, ok := value.(string)
		if !ok || (name != "title" && name != "body") {
			return nil, nil, fmt.Errorf("Memory properties admit only title and body JSON strings")
		}
		if name == "title" {
			title = &text
		} else {
			body = &text
		}
	}
	return title, body, nil
}

func runGraphPreviewUpdateMemory(cmd *cobra.Command, path string) error {
	if err := graphPreviewFlags(cmd, "properties", "metadata", "set-metadata", "unset-metadata", "if-revision", "force"); err != nil {
		return err
	}
	if !cmd.Flags().Changed("properties") {
		return graphFailure("invalid_properties", "Memory update requires --properties to merge named top-level properties", 2)
	}
	revision, unconditional, err := graphPreviewEditRevisionGuard(cmd)
	if err != nil {
		return err
	}
	input, _ := cmd.Flags().GetString("properties")
	values, err := graphPreviewProperties(input, cmd.InOrStdin())
	if err != nil {
		return graphFailure("invalid_properties", err.Error(), 2)
	}
	metadata, err := graphPreviewMetadataPatch(cmd)
	if err != nil {
		return err
	}
	issueUpdated := false
	err = withGraphStore(func(ctx context.Context, store *graphstore.Store) (any, string, error) {
		current, err := store.Read(ctx, path)
		if err != nil {
			return nil, "", err
		}
		if _, ok := current.(graphstore.IssueRecord); ok {
			request := graphstore.UpdateIssueRequest{Path: path, Actor: getActorWithGit(), ExpectedRevision: revision, Unconditional: unconditional, Metadata: metadata}
			request.ForceNotesOverwrite, _ = cmd.Flags().GetBool("force")
			if err := graphPreviewApplyIssueProperties(cmd, &request, values); err != nil {
				return nil, "", err
			}
			result, err := store.UpdateIssue(ctx, request)
			if err != nil {
				return nil, "", err
			}
			issueUpdated = true
			verb := "Updated"
			if !result.Changed {
				verb = "Unchanged"
			}
			return result, fmt.Sprintf("%s %s", verb, result.Issue.ID), nil
		}
		if cmd.Flags().Changed("force") {
			return nil, "", graphFailure("invalid_properties", "--force applies only to an Issue notes edit", 2)
		}
		title, body, err := graphPreviewMemoryProperties(values)
		if err != nil {
			return nil, "", graphFailure("invalid_properties", err.Error(), 2)
		}
		result, err := store.PatchMemory(ctx, graphstore.MemoryPatchRequest{Path: path, Title: title, Body: body, PropertiesProvided: true, Metadata: metadata, Actor: getActorWithGit(), ExpectedRevision: revision, Unconditional: unconditional})
		verb := "Updated"
		if !result.Changed {
			verb = "Unchanged"
		}
		return graphPreviewReplacementResult(result, fmt.Sprintf("%s %s", verb, result.Memory.ID), result.Replaced, err)
	})
	if err == nil && issueUpdated {
		SetLastTouchedID(path)
	}
	return err
}
