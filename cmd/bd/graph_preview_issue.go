package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage/graphstore"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
	"github.com/steveyegge/beads/internal/validation"
	publicops "github.com/steveyegge/beads/issueops"
)

// This disposable adapter preserves the Issue writer and classification rules.
// Its canonical path is distinct from the internal backing ID; no alias or
// production nominal-Type contract is established by this preview.
func runGraphPreviewCreateIssue(cmd *cobra.Command, args []string) error {
	if err := graphPreviewWritePolicy(); err != nil {
		return err
	}
	beadType, err := graphPreviewCreateBeadType(cmd, graphPreviewConfig.GraphScopeURL)
	if err != nil {
		return err
	}
	if beadType == graphstore.MemoryTypeURL(graphPreviewConfig.GraphScopeURL) {
		return runGraphPreviewCreateMemory(cmd, args)
	}
	if err := graphPreviewFlags(cmd, "bead-type", "id", "title", "description", "body", "message", "type", "priority", "labels", "label", "design", "acceptance", "assignee", "estimate", "external-ref", "spec-id", "notes", "due", "metadata", "properties"); err != nil {
		return err
	}
	properties, err := graphPreviewCreateProperties(cmd)
	if err != nil {
		return err
	}
	path, err := graphPreviewCreateBeadPath(cmd)
	if err != nil {
		return err
	}
	titleFlag, _ := cmd.Flags().GetString("title")
	if value, ok := properties["title"]; ok {
		if len(args) != 0 || cmd.Flags().Changed("title") {
			return graphFailure("invalid_properties", "Issue title was supplied by both --properties and a shorthand", 2)
		}
		text, ok := value.(string)
		if !ok {
			return graphFailure("invalid_properties", "Issue title must be a JSON string", 2)
		}
		titleFlag = text
	}
	title, err := resolveTitle(args, titleFlag, "", "")
	if err != nil {
		return graphFailure("invalid_properties", err.Error(), 2)
	}
	// Ordinary create treats "-" as stdin; this bounded graph route accepts
	// inline Issue text only and refuses before acquiring input or opening storage.
	for _, name := range []string{"description", "body", "message"} {
		value, _ := cmd.Flags().GetString(name)
		if cmd.Flags().Changed(name) && value == "-" {
			return graphFailure("capability_unavailable", "graph Issue create currently accepts inline description text only; stdin and file sources are unavailable", 5)
		}
	}
	description, _, err := getDescriptionFlag(cmd)
	if err != nil {
		return err
	}
	if value, ok := properties["description"]; ok {
		if cmd.Flags().Changed("description") || cmd.Flags().Changed("body") || cmd.Flags().Changed("message") {
			return graphFailure("invalid_properties", "Issue description was supplied by both --properties and a shorthand flag", 2)
		}
		text, ok := value.(string)
		if !ok {
			return graphFailure("invalid_properties", "Issue description must be a JSON string", 2)
		}
		description = text
	}
	if description == "" && config.GetBool("create.require-description") {
		return graphFailure("invalid_properties", "description is required by create.require-description", 2)
	}
	priorityText, _ := cmd.Flags().GetString("priority")
	if value, ok := properties["priority"]; ok {
		if cmd.Flags().Changed("priority") {
			return graphFailure("invalid_properties", "Issue priority was supplied by both --properties and --priority", 2)
		}
		integer, err := graphPreviewIssuePropertyInt("priority", value)
		if err != nil {
			return err
		}
		priorityText = strconv.Itoa(integer)
	}
	priority, err := validation.ValidatePriority(priorityText)
	if err != nil {
		return graphFailure("invalid_properties", err.Error(), 2)
	}
	classification, _ := cmd.Flags().GetString("type")
	if value, ok := properties["issue_type"]; ok {
		if cmd.Flags().Changed("type") {
			return graphFailure("invalid_properties", "Issue type was supplied by both --properties and --type", 2)
		}
		text, ok := value.(string)
		if !ok {
			return graphFailure("invalid_properties", "Issue issue_type must be a JSON string", 2)
		}
		classification = text
	}
	issueType := types.IssueType(classification).Normalize()
	storageClass, err := resolveStorageClass("", issueType)
	if err != nil {
		return graphFailure("invalid_properties", err.Error(), 2)
	}
	if storageClass.Normalize() != "" {
		return graphFailure("capability_unavailable", "graph Issue preview requires versioned storage; configured ephemeral or unversioned storage is unsupported", 5)
	}
	labels, _ := cmd.Flags().GetStringSlice("labels")
	aliasLabels, _ := cmd.Flags().GetStringSlice("label")
	creator := getActorWithGit()
	request := publicops.CreateRequest{Actor: creator, Issue: &types.Issue{
		CreatedBy: creator, Owner: getOwner(),
		Title: title, Description: description, Status: types.StatusOpen,
		Priority: priority, IssueType: issueType, Labels: utils.NormalizeLabels(append(labels, aliasLabels...)),
	}}
	request.Issue.Metadata, err = graphPreviewMetadataCreate(cmd)
	if err != nil {
		return err
	}
	if err := graphPreviewIssueCreateFields(cmd, request.Issue); err != nil {
		return err
	}
	if err := graphPreviewApplyIssueCreateProperties(cmd, request.Issue, properties); err != nil {
		return err
	}
	err = withGraphStore(func(ctx context.Context, store *graphstore.Store) (any, string, error) {
		record, err := store.CreateIssue(ctx, path, request)
		if err != nil {
			return nil, "", err
		}
		return record, fmt.Sprintf("Created %s\n", path), nil
	})
	if err == nil {
		SetLastTouchedID(path)
	}
	return err
}

// Initial assignment is an open Issue property, not a claim. Keep optional
// estimate presence and the ordinary CLI's empty-external-reference convention.
func graphPreviewIssueCreateFields(cmd *cobra.Command, issue *types.Issue) error {
	for _, field := range []struct {
		name string
		dest *string
	}{
		{"design", &issue.Design}, {"acceptance", &issue.AcceptanceCriteria}, {"notes", &issue.Notes},
		{"assignee", &issue.Assignee}, {"spec-id", &issue.SpecID},
	} {
		value, _ := cmd.Flags().GetString(field.name)
		if !utf8.ValidString(value) {
			return graphFailure("invalid_properties", "Issue "+field.name+" must be valid UTF-8", 2)
		}
		*field.dest = value
	}
	if err := types.CheckFieldLen("assignee", issue.Assignee); err != nil {
		return graphFailure("invalid_properties", err.Error(), 2)
	}
	external, _ := cmd.Flags().GetString("external-ref")
	if !utf8.ValidString(external) {
		return graphFailure("invalid_properties", "Issue external-ref must be valid UTF-8", 2)
	}
	if external != "" {
		issue.ExternalRef = &external
	}
	if cmd.Flags().Changed("estimate") {
		value, err := cmd.Flags().GetInt("estimate")
		if err != nil {
			return graphFailure("invalid_properties", err.Error(), 2)
		}
		if err := types.ValidateIssueEstimatedMinutes(&value); err != nil {
			return graphFailure("invalid_properties", err.Error(), 2)
		}
		issue.EstimatedMinutes = &value
	}
	if cmd.Flags().Changed("due") {
		due, err := graphPreviewIssueDueInput(cmd)
		if err != nil {
			return err
		}
		issue.DueAt = due
	}
	return nil
}

func graphPreviewIssuePropertyInt(name string, value any) (int, error) {
	number, ok := value.(float64)
	if !ok {
		return 0, graphFailure("invalid_properties", "Issue "+name+" must be a JSON integer", 2)
	}
	integer := int(number)
	if float64(integer) != number {
		return 0, graphFailure("invalid_properties", "Issue "+name+" must be a representable JSON integer", 2)
	}
	return integer, nil
}

// Property-object creation uses the installed Issue Type's JSON field names.
// Legacy text flags remain adapters, and duplicate sources refuse rather than
// silently choosing precedence. Lifecycle, derived and immutable fields remain
// owned by the native Issue writer and cannot be initialized through this path.
func graphPreviewApplyIssueCreateProperties(cmd *cobra.Command, issue *types.Issue, properties map[string]any) error {
	for name, value := range properties {
		switch name {
		case "title", "description", "priority", "issue_type":
			continue // Resolved before constructing the Issue.
		case "design", "acceptance_criteria", "notes", "spec_id", "assignee":
			flag := map[string]string{"design": "design", "acceptance_criteria": "acceptance", "notes": "notes", "spec_id": "spec-id", "assignee": "assignee"}[name]
			if cmd.Flags().Changed(flag) {
				return graphFailure("invalid_properties", "Issue "+name+" was supplied by both --properties and --"+flag, 2)
			}
			text, ok := value.(string)
			if !ok {
				return graphFailure("invalid_properties", "Issue "+name+" must be a JSON string", 2)
			}
			switch name {
			case "design":
				issue.Design = text
			case "acceptance_criteria":
				issue.AcceptanceCriteria = text
			case "notes":
				issue.Notes = text
			case "spec_id":
				issue.SpecID = text
			case "assignee":
				issue.Assignee = text
				if err := types.CheckFieldLen("assignee", text); err != nil {
					return graphFailure("invalid_properties", err.Error(), 2)
				}
			}
		case "labels":
			if cmd.Flags().Changed("labels") || cmd.Flags().Changed("label") {
				return graphFailure("invalid_properties", "Issue labels were supplied by both --properties and a shorthand flag", 2)
			}
			values, ok := value.([]any)
			if !ok {
				return graphFailure("invalid_properties", "Issue labels must be a JSON string array", 2)
			}
			labels := make([]string, len(values))
			for i, item := range values {
				text, ok := item.(string)
				if !ok {
					return graphFailure("invalid_properties", "Issue labels must be a JSON string array", 2)
				}
				labels[i] = text
			}
			issue.Labels = utils.NormalizeLabels(labels)
		case "estimated_minutes":
			if cmd.Flags().Changed("estimate") {
				return graphFailure("invalid_properties", "Issue estimated_minutes was supplied by both --properties and --estimate", 2)
			}
			if value == nil {
				issue.EstimatedMinutes = nil
				continue
			}
			integer, err := graphPreviewIssuePropertyInt(name, value)
			if err != nil {
				return err
			}
			if err := types.ValidateIssueEstimatedMinutes(&integer); err != nil {
				return graphFailure("invalid_properties", err.Error(), 2)
			}
			issue.EstimatedMinutes = &integer
		case "external_ref":
			if cmd.Flags().Changed("external-ref") {
				return graphFailure("invalid_properties", "Issue external_ref was supplied by both --properties and --external-ref", 2)
			}
			if value == nil {
				issue.ExternalRef = nil
				continue
			}
			text, ok := value.(string)
			if !ok {
				return graphFailure("invalid_properties", "Issue external_ref must be a JSON string or null", 2)
			}
			if text == "" {
				issue.ExternalRef = nil // Match the established --external-ref empty-value convention.
			} else {
				issue.ExternalRef = &text
			}
		case "due_at":
			if cmd.Flags().Changed("due") {
				return graphFailure("invalid_properties", "Issue due_at was supplied by both --properties and --due", 2)
			}
			if value == nil {
				issue.DueAt = nil
				continue
			}
			text, ok := value.(string)
			if !ok {
				return graphFailure("invalid_properties", "Issue due_at must be an RFC 3339 JSON string or null", 2)
			}
			due, err := time.Parse(time.RFC3339Nano, text)
			if err != nil {
				return graphFailure("invalid_properties", "Issue due_at must be an RFC 3339 timestamp", 2)
			}
			issue.DueAt = &due
		default:
			return graphFailure("invalid_properties", "Issue property "+name+" is not writable during creation", 2)
		}
	}
	return nil
}

// Nominal bead type is separate from the Issue classification selected by -t.
// Only the two installed graph descriptors have a create writer in this slice.
func graphPreviewCreateBeadType(cmd *cobra.Command, scope string) (string, error) {
	if !cmd.Flags().Changed("bead-type") {
		return graphstore.IssueTypeURL(scope), nil
	}
	selector, _ := cmd.Flags().GetString("bead-type")
	typeURL, err := graphPreviewTypeURL(scope, selector)
	if err != nil {
		return "", graphFailure("invalid_selector", err.Error(), 2)
	}
	if typeURL != graphstore.IssueTypeURL(scope) && typeURL != graphstore.MemoryTypeURL(scope) {
		return "", graphFailure("capability_unavailable", "graph create supports only the installed Issue and Memory bead types", 5)
	}
	return typeURL, nil
}

func graphPreviewCreateProperties(cmd *cobra.Command) (map[string]any, error) {
	if !cmd.Flags().Changed("properties") {
		return nil, nil
	}
	input, _ := cmd.Flags().GetString("properties")
	properties, err := graphPreviewProperties(input, cmd.InOrStdin())
	if err != nil {
		return nil, graphFailure("invalid_properties", err.Error(), 2)
	}
	return properties, nil
}

func runGraphPreviewCreateMemory(cmd *cobra.Command, args []string) error {
	title, body, err := graphPreviewCreateMemoryInput(cmd, args)
	if err != nil {
		return err
	}
	path, err := graphPreviewCreateBeadPath(cmd)
	if err != nil {
		return err
	}
	metadata, err := graphPreviewMetadataCreate(cmd)
	if err != nil {
		return err
	}
	return withGraphStore(func(ctx context.Context, store *graphstore.Store) (any, string, error) {
		record, err := store.Create(ctx, graphstore.CreateRequest{Path: path, Title: title, Body: body, Actor: getActorWithGit(), Metadata: metadata})
		if err != nil {
			return nil, "", err
		}
		return record, fmt.Sprintf("Created %s\n", path), nil
	})
}

// Inline text only: validate aliases and explicit presence before the ordinary
// description helper can read stdin. Issue-only flags cannot be silently lost.
func graphPreviewCreateMemoryInput(cmd *cobra.Command, args []string) (string, string, error) {
	if err := graphPreviewFlags(cmd, "bead-type", "id", "title", "description", "body", "message", "metadata", "properties"); err != nil {
		return "", "", err
	}
	properties, err := graphPreviewCreateProperties(cmd)
	if err != nil {
		return "", "", err
	}
	for name := range properties {
		if name != "title" && name != "body" {
			return "", "", graphFailure("invalid_properties", "Memory properties do not include "+name, 2)
		}
		if _, ok := properties[name].(string); !ok {
			return "", "", graphFailure("invalid_properties", "Memory "+name+" must be a JSON string", 2)
		}
	}
	if len(args) > 1 {
		return "", "", graphFailure("invalid_selector", "Memory create accepts at most one title", 2)
	}
	bodyPresent, bodyValue := false, ""
	for _, name := range []string{"description", "body", "message"} {
		if !cmd.Flags().Changed(name) {
			continue
		}
		value, _ := cmd.Flags().GetString(name)
		if value == "-" {
			return "", "", graphFailure("capability_unavailable", "graph Memory create accepts inline body text only; stdin and file sources are unavailable", 5)
		}
		if bodyPresent && value != bodyValue {
			return "", "", graphFailure("invalid_properties", "Memory create description/body/message aliases must have the same value", 2)
		}
		bodyPresent, bodyValue = true, value
	}
	if value, ok := properties["body"]; ok {
		if bodyPresent {
			return "", "", graphFailure("invalid_properties", "Memory body was supplied by both --properties and a shorthand flag", 2)
		}
		bodyPresent, bodyValue = true, value.(string)
	}
	body, _, err := getDescriptionFlag(cmd)
	if err != nil {
		return "", "", err
	}
	if _, ok := properties["body"]; ok {
		body = bodyValue
	}
	title, _ := cmd.Flags().GetString("title")
	_, propertyTitlePresent := properties["title"]
	if value, ok := properties["title"]; ok {
		if len(args) > 0 || cmd.Flags().Changed("title") {
			return "", "", graphFailure("invalid_properties", "Memory title was supplied by both --properties and a shorthand", 2)
		}
		title = value.(string)
	}
	if (cmd.Flags().Changed("title") || propertyTitlePresent) && strings.TrimSpace(title) == "" {
		return "", "", graphFailure("invalid_properties", "an explicit --title must be nonempty", 2)
	}
	if len(args) > 0 || cmd.Flags().Changed("title") || propertyTitlePresent {
		title, err = resolveTitle(args, title, "", "")
		if err != nil {
			return "", "", graphFailure("invalid_properties", err.Error(), 2)
		}
	} else if bodyPresent {
		title = graphPreviewMemoryTitle(body)
	} else {
		return "", "", graphFailure("invalid_properties", "Memory create requires a title or inline body", 2)
	}
	if !utf8.ValidString(title) || !utf8.ValidString(body) {
		return "", "", graphFailure("invalid_properties", "Memory title and body must be valid UTF-8", 2)
	}
	return title, body, nil
}
