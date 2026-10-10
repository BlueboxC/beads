package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	graph "github.com/steveyegge/beads/graphops"
	"github.com/steveyegge/beads/internal/storage/graphstore"
)

// A disposable CLI input budget, advertised by status --graph. It is not a
// negotiated BDP property limit and does not establish the production contract.
const graphPreviewPropertiesLimit = 1 << 20

func graphPreviewProperties(input string, stdin io.Reader) (map[string]any, error) {
	var reader io.Reader = strings.NewReader(input)
	if strings.HasPrefix(input, "@") {
		if input == "@-" {
			reader = stdin
		} else {
			file, err := os.Open(strings.TrimPrefix(input, "@")) // #nosec G304 -- explicit operator-selected properties file
			if err != nil {
				return nil, err
			}
			defer func() { _ = file.Close() }()
			reader = file
		}
	}
	raw, err := io.ReadAll(io.LimitReader(reader, graphPreviewPropertiesLimit+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > graphPreviewPropertiesLimit {
		return nil, fmt.Errorf("Resource properties input exceeds the preview limit of %d bytes", graphPreviewPropertiesLimit)
	}
	// Validate before decoding: encoding/json by itself silently accepts duplicate
	// members and repairs invalid Unicode. The existing graph admission rejects both.
	canonical, err := graph.CanonicalizeJSON(raw)
	if err != nil {
		return nil, err
	}
	var properties map[string]any
	if err := json.Unmarshal(canonical, &properties); err != nil || properties == nil {
		return nil, fmt.Errorf("Resource properties must be a JSON object")
	}
	return properties, nil
}

func graphPreviewRevisionGuard(cmd *cobra.Command, source, required bool) (string, bool, error) {
	revisionFlag, unconditionalFlag := "if-revision", "unconditional"
	if source {
		revisionFlag, unconditionalFlag = "if-source-revision", "unconditional-source"
	}
	hasRevision, hasUnconditional := cmd.Flags().Changed(revisionFlag), cmd.Flags().Changed(unconditionalFlag)
	if !hasRevision && !hasUnconditional && !required {
		// Source guards are opt-in at the CLI; the writer still validates
		// every supplied token and atomically accepts the actual predecessor.
		return "", source, nil
	}
	revision, _ := cmd.Flags().GetString(revisionFlag)
	unconditional, _ := cmd.Flags().GetBool(unconditionalFlag)
	if hasRevision == hasUnconditional || (hasRevision && revision == "") || (hasUnconditional && !unconditional) {
		return "", false, graphFailure("invalid_selector", "choose either --"+revisionFlag+" REVISION or --"+unconditionalFlag, 2)
	}
	return revision, unconditional, nil
}

// Ordinary edits use the current predecessor when no comparison was asked for.
// The writer still checks a supplied token atomically and records the actual
// replaced version. Destructive commands retain their separate explicit guard.
func graphPreviewEditRevisionGuard(cmd *cobra.Command) (string, bool, error) {
	if !cmd.Flags().Changed("if-revision") {
		return "", true, nil
	}
	revision, _ := cmd.Flags().GetString("if-revision")
	if revision == "" {
		return "", false, graphFailure("invalid_selector", "--if-revision requires a nonempty revision token", 2)
	}
	return revision, false, nil
}

// The old name remains a hidden compatibility alias for existing scripts.
func registerGraphLinkTypeFlag(cmd *cobra.Command) {
	cmd.Flags().String("link-type", "", "Installed Link Type: types/NAME or full local URL (graph preview only)")
	cmd.Flags().String("resource-type", "", "Compatibility alias for --link-type")
	_ = cmd.Flags().MarkHidden("resource-type")
}

func graphPreviewLinkTypeChanged(cmd *cobra.Command) bool {
	return cmd.Flags().Changed("link-type") || cmd.Flags().Changed("resource-type")
}

func graphPreviewTypeURL(scope, selector string) (string, error) {
	if strings.HasPrefix(selector, "types/") {
		selector = scope + selector
	}
	prefix := scope + "types/"
	if !strings.HasPrefix(selector, prefix) {
		return "", fmt.Errorf("Type must be types/NAME or a canonical Type URL in this Scope")
	}
	for _, segment := range strings.Split(strings.TrimPrefix(selector, prefix), "/") {
		if err := graph.ValidateCanonicalSegment(segment); err != nil {
			return "", fmt.Errorf("invalid local Type path: %w", err)
		}
	}
	if err := graph.ValidateTypeURL(selector); err != nil {
		return "", err
	}
	return selector, nil
}

func graphPreviewLinkType(cmd *cobra.Command) (string, error) {
	if cmd.Flags().Changed("link-type") && cmd.Flags().Changed("resource-type") {
		return "", graphFailure("invalid_selector", "choose --link-type once; do not combine it with --resource-type", 2)
	}
	if !graphPreviewLinkTypeChanged(cmd) {
		return "", nil
	}
	flag := "link-type"
	if cmd.Flags().Changed("resource-type") {
		flag = "resource-type"
	}
	selector, _ := cmd.Flags().GetString(flag)
	typ, err := graphPreviewTypeURL(graphPreviewConfig.GraphScopeURL, selector)
	if err != nil {
		return "", graphFailure("invalid_selector", err.Error(), 2)
	}
	return typ, nil
}

func runGraphPreviewLink(cmd *cobra.Command, args []string) error {
	typ, err := graphPreviewLinkType(cmd)
	if err != nil {
		return err
	}
	if typ == "" || typ == graphstore.DependencyTypeURL(graphPreviewConfig.GraphScopeURL) {
		return runGraphPreviewAddDependency(cmd, args)
	}
	if err := graphPreviewWritePolicy(); err != nil {
		return err
	}
	// --type is deliberately absent: it belongs to the blocking Dependency route
	// above, so this list refuses it before anything below could.
	if err := graphPreviewFlags(cmd, "link-type", "resource-type", "id", "properties", "metadata", "if-source-revision", "unconditional-source"); err != nil {
		return err
	}
	if !graphstore.IsInformationalTypeURL(graphPreviewConfig.GraphScopeURL, typ) {
		return graphFailure("capability_unavailable", "Link Type is not supported by this preview", 5)
	}
	if len(args) != 2 {
		return graphFailure("invalid_selector", "graph Link creation requires two Bead IDs or beads/PATH selectors", 2)
	}
	paths := make([]string, 2)
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
	path, _ := cmd.Flags().GetString("id")
	if cmd.Flags().Changed("id") {
		path, err = graphPreviewLinkPath(graphPreviewConfig.GraphScopeURL, path)
		if err != nil {
			return graphFailure("invalid_selector", err.Error(), 2)
		}
	}
	revision, unconditional, err := graphPreviewRevisionGuard(cmd, true, false)
	if err != nil {
		return err
	}
	properties := map[string]any{}
	if cmd.Flags().Changed("properties") {
		input, _ := cmd.Flags().GetString("properties")
		properties, err = graphPreviewProperties(input, cmd.InOrStdin())
		if err != nil {
			return graphFailure("invalid_properties", err.Error(), 2)
		}
	}
	metadata, err := graphPreviewMetadataCreate(cmd)
	if err != nil {
		return err
	}
	return withGraphStore(func(ctx context.Context, store *graphstore.Store) (any, string, error) {
		result, err := store.AddInformationalLink(ctx, graphstore.LinkCreateRequest{
			Path: path, TypeURL: typ, SourcePath: paths[0], TargetPath: paths[1], Properties: properties, Metadata: metadata,
			Actor: getActorWithGit(), ExpectedSourceRevision: revision, UnconditionalSource: unconditional,
		})
		return graphPreviewReplacementResult(result, fmt.Sprintf("Created %s: %s → %s", result.Link.ID, args[0], args[1]), result.ReplacedSource, err)
	})
}

func runGraphPreviewUpdateLink(cmd *cobra.Command, args []string) error {
	if err := graphPreviewWritePolicy(); err != nil {
		return err
	}
	if err := graphPreviewFlags(cmd, "properties", "metadata", "set-metadata", "unset-metadata", "if-revision", "if-source-revision", "unconditional-source"); err != nil {
		return err
	}
	if len(args) != 1 {
		return graphFailure("invalid_selector", "graph update requires one canonical links/PATH", 2)
	}
	path, err := graphPreviewResourcePath(graphPreviewConfig.GraphScopeURL, args[0])
	if err != nil {
		return graphFailure("invalid_selector", err.Error(), 2)
	}
	if err := graph.ValidateLinkPath(path); err != nil {
		return graphFailure("capability_unavailable", "this preview updates informational Link properties only", 5)
	}
	if !cmd.Flags().Changed("properties") {
		return graphFailure("invalid_properties", "Link update requires --properties to merge named top-level properties", 2)
	}
	revision, unconditional, err := graphPreviewEditRevisionGuard(cmd)
	if err != nil {
		return err
	}
	sourceRevision, unconditionalSource, err := graphPreviewRevisionGuard(cmd, true, false)
	if err != nil {
		return err
	}
	input, _ := cmd.Flags().GetString("properties")
	properties, err := graphPreviewProperties(input, cmd.InOrStdin())
	if err != nil {
		return graphFailure("invalid_properties", err.Error(), 2)
	}
	metadata, err := graphPreviewMetadataPatch(cmd)
	if err != nil {
		return err
	}
	return withGraphStore(func(ctx context.Context, store *graphstore.Store) (any, string, error) {
		result, err := store.UpdateLink(ctx, graphstore.LinkUpdateRequest{
			Path: path, Properties: properties, MergeProperties: true, Metadata: metadata, Actor: getActorWithGit(), ExpectedRevision: revision,
			Unconditional: unconditional, ExpectedSourceRevision: sourceRevision, UnconditionalSource: unconditionalSource,
		})
		verb := "Updated"
		if !result.Changed {
			verb = "Unchanged"
		}
		return graphPreviewReplacementResult(result, fmt.Sprintf("%s %s", verb, result.Link.ID), result.ReplacedSource, err)
	})
}
