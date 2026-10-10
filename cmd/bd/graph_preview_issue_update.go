package main

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage/graphstore"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/validation"
	publicops "github.com/steveyegge/beads/issueops"
)

var graphPreviewIssueEditFlags = []string{"title", "description", "body", "message", "design", "acceptance", "priority", "assignee", "notes", "clear-notes", "append-notes", "estimate", "external-ref", "spec-id", "due"}

func graphPreviewIssueEditFlagsChanged(cmd *cobra.Command) bool {
	for _, name := range graphPreviewIssueEditFlags {
		if cmd.Flags().Changed(name) {
			return true
		}
	}
	return false
}

// This narrow preview keeps familiar Issue field meanings while requiring the
// existing opaque graph guard for a canonical Resource selector. File/stdin and
// broader workflow flags remain explicit refusals; legacy routing is unchanged.
func graphPreviewIssueEditRequest(cmd *cobra.Command, path string) (graphstore.UpdateIssueRequest, error) {
	request := graphstore.UpdateIssueRequest{Path: path}
	allowed := append([]string{"if-revision", "force", "metadata", "set-metadata", "unset-metadata", "properties"}, graphPreviewIssueEditFlags...)
	if err := graphPreviewFlags(cmd, allowed...); err != nil {
		return request, err
	}
	if !graphPreviewIssueEditFlagsChanged(cmd) && !graphPreviewMetadataFlagsChanged(cmd) && !cmd.Flags().Changed("properties") {
		return request, graphFailure("invalid_properties", "Issue update requires at least one supported field", 2)
	}
	metadata, err := graphPreviewMetadataPatch(cmd)
	if err != nil {
		return request, err
	}
	request.Metadata = metadata
	revision, unconditional, err := graphPreviewEditRevisionGuard(cmd)
	if err != nil {
		return request, err
	}
	request.ExpectedRevision, request.Unconditional = revision, unconditional
	if cmd.Flags().Changed("notes") && (cmd.Flags().Changed("clear-notes") || cmd.Flags().Changed("append-notes")) ||
		cmd.Flags().Changed("clear-notes") && cmd.Flags().Changed("append-notes") {
		return request, graphFailure("invalid_properties", "--notes, --clear-notes and --append-notes are mutually exclusive", 2)
	}
	if cmd.Flags().Changed("clear-notes") && !clearNotesRequested(cmd) {
		return request, graphFailure("invalid_properties", "--clear-notes=false does not request an edit", 2)
	}
	if cmd.Flags().Changed("force") && !cmd.Flags().Changed("notes") && !cmd.Flags().Changed("properties") {
		return request, graphFailure("invalid_properties", "graph Issue update accepts --force only with a notes edit", 2)
	}
	request.ForceNotesOverwrite, _ = cmd.Flags().GetBool("force")
	if clearNotesRequested(cmd) {
		cleared := ""
		request.Notes = &cleared
	}
	if cmd.Flags().Changed("due") {
		value, err := graphPreviewIssueDueInput(cmd)
		if err != nil {
			return request, err
		}
		request.DueAt = publicops.Field[*time.Time]{Set: true, Value: value}
	}

	if cmd.Flags().Changed("priority") {
		value, _ := cmd.Flags().GetString("priority")
		priority, err := validation.ValidatePriority(value)
		if err != nil {
			return request, graphFailure("invalid_properties", err.Error(), 2)
		}
		request.Priority = &priority
	}
	if cmd.Flags().Changed("estimate") {
		value, err := cmd.Flags().GetInt("estimate")
		if err != nil {
			return request, graphFailure("invalid_properties", err.Error(), 2)
		}
		if err := types.ValidateIssueEstimatedMinutes(&value); err != nil {
			return request, graphFailure("invalid_properties", err.Error(), 2)
		}
		request.EstimatedMinutes = &value
	}
	if cmd.Flags().Changed("assignee") {
		value, _ := cmd.Flags().GetString("assignee")
		if !utf8.ValidString(value) {
			return request, graphFailure("invalid_properties", "Issue assignee must be valid UTF-8", 2)
		}
		if err := types.CheckFieldLen("assignee", value); err != nil {
			return request, graphFailure("invalid_properties", err.Error(), 2)
		}
		// Preserve the existing update's literal value. A graph revision guard
		// does not grant permission to take another actor's active assignment.
		request.Assignee = &value
	}

	fields := []struct {
		name string
		out  **string
	}{
		{"title", &request.Title}, {"description", &request.Description},
		{"body", &request.Description}, {"message", &request.Description},
		{"design", &request.Design},
		{"acceptance", &request.AcceptanceCriteria},
		{"notes", &request.Notes},
		{"append-notes", &request.AppendNotes},
		{"external-ref", &request.ExternalRef}, {"spec-id", &request.SpecID},
	}
	for _, field := range fields {
		if !cmd.Flags().Changed(field.name) {
			continue
		}
		value, _ := cmd.Flags().GetString(field.name)
		if !utf8.ValidString(value) {
			return request, graphFailure("invalid_properties", "Issue "+field.name+" must be valid UTF-8", 2)
		}
		if field.name == "notes" {
			if err := validateNotesUpdate(value); err != nil {
				return request, graphFailure("invalid_properties", err.Error(), 2)
			}
		}
		if value == "-" && field.out == &request.Description {
			return request, graphFailure("capability_unavailable", "graph Issue update currently accepts inline text only; stdin and file sources are unavailable", 5)
		}
		if field.name == "title" {
			value = strings.TrimSpace(value)
			if err := types.ValidateIssueTitle(value); err != nil {
				return request, graphFailure("invalid_properties", err.Error(), 2)
			}
		}
		if *field.out != nil && **field.out != value {
			return request, graphFailure("invalid_properties", "description aliases must have identical values", 2)
		}
		*field.out = &value
	}
	return request, nil
}

// The JSON object names native Issue properties, while the existing Issue
// writer keeps ownership of validation, no-ops, History and assignment fences.
func graphPreviewIssuePropertyMerge(cmd *cobra.Command, request *graphstore.UpdateIssueRequest) error {
	if !cmd.Flags().Changed("properties") {
		return nil
	}
	input, _ := cmd.Flags().GetString("properties")
	values, err := graphPreviewProperties(input, cmd.InOrStdin())
	if err != nil {
		return graphFailure("invalid_properties", err.Error(), 2)
	}
	return graphPreviewApplyIssueProperties(cmd, request, values)
}

func graphPreviewApplyIssueProperties(cmd *cobra.Command, request *graphstore.UpdateIssueRequest, values map[string]any) error {
	request.PropertiesProvided = true
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		value := values[name]
		if dest := map[string]**string{
			"title": &request.Title, "description": &request.Description,
			"design": &request.Design, "acceptance_criteria": &request.AcceptanceCriteria,
			"notes": &request.Notes, "assignee": &request.Assignee,
			"spec_id": &request.SpecID, "external_ref": &request.ExternalRef,
		}[name]; dest != nil {
			if *dest != nil || name == "notes" && request.AppendNotes != nil {
				return graphFailure("invalid_properties", "Issue "+name+" was supplied by both --properties and a shorthand flag", 2)
			}
			if name == "description" && (cmd.Flags().Changed("body") || cmd.Flags().Changed("message")) {
				return graphFailure("invalid_properties", "Issue description was supplied by both --properties and a shorthand flag", 2)
			}
			if value == nil && name == "external_ref" {
				cleared := ""
				*dest = &cleared
				continue
			}
			text, ok := value.(string)
			if !ok {
				return graphFailure("invalid_properties", "Issue "+name+" must be a JSON string", 2)
			}
			if name == "title" {
				text = strings.TrimSpace(text)
				if err := types.ValidateIssueTitle(text); err != nil {
					return graphFailure("invalid_properties", err.Error(), 2)
				}
			}
			if name == "assignee" {
				if err := types.CheckFieldLen("assignee", text); err != nil {
					return graphFailure("invalid_properties", err.Error(), 2)
				}
			}
			if name == "notes" {
				if err := validateNotesUpdate(text); err != nil {
					return graphFailure("invalid_properties", err.Error(), 2)
				}
			}
			*dest = &text
			continue
		}
		switch name {
		case "priority":
			if request.Priority != nil {
				return graphFailure("invalid_properties", "Issue priority was supplied by both --properties and --priority", 2)
			}
			integer, err := graphPreviewIssuePropertyInt(name, value)
			if err != nil {
				return err
			}
			priority, err := validation.ValidatePriority(strconv.Itoa(integer))
			if err != nil {
				return graphFailure("invalid_properties", err.Error(), 2)
			}
			request.Priority = &priority
		case "estimated_minutes":
			if request.EstimatedMinutes != nil {
				return graphFailure("invalid_properties", "Issue estimated_minutes was supplied by both --properties and --estimate", 2)
			}
			if value == nil {
				request.ClearEstimatedMinutes = true
				continue
			}
			integer, err := graphPreviewIssuePropertyInt(name, value)
			if err != nil {
				return err
			}
			if err := types.ValidateIssueEstimatedMinutes(&integer); err != nil {
				return graphFailure("invalid_properties", err.Error(), 2)
			}
			request.EstimatedMinutes = &integer
		case "due_at":
			if request.DueAt.Set {
				return graphFailure("invalid_properties", "Issue due_at was supplied by both --properties and --due", 2)
			}
			if value == nil {
				request.DueAt = publicops.Field[*time.Time]{Set: true}
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
			request.DueAt = publicops.Field[*time.Time]{Set: true, Value: &due}
		default:
			return graphFailure("invalid_properties", "Issue property "+name+" is not writable through update", 2)
		}
	}
	if cmd.Flags().Changed("force") && !cmd.Flags().Changed("notes") && request.Notes == nil {
		return graphFailure("invalid_properties", "graph Issue update accepts --force only with a notes edit", 2)
	}
	return nil
}

func runGraphPreviewUpdateIssue(cmd *cobra.Command, path string) error {
	request, err := graphPreviewIssueEditRequest(cmd, path)
	if err != nil {
		return err
	}
	if err := graphPreviewIssuePropertyMerge(cmd, &request); err != nil {
		return err
	}
	request.Actor = getActorWithGit()
	err = withGraphStore(func(ctx context.Context, store *graphstore.Store) (any, string, error) {
		result, err := store.UpdateIssue(ctx, request)
		if err != nil {
			return nil, "", err
		}
		verb := "Unchanged"
		if result.Changed {
			verb = "Updated"
		}
		return result, fmt.Sprintf("%s %s", verb, result.Issue.ID), nil
	})
	if err == nil {
		SetLastTouchedID(path)
	}
	return err
}
