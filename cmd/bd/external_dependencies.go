package main

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/issueops"
)

// configuredExternalResolver pins configured paths when the workspace is loaded;
// each evaluation opens and closes providers without migrations or auto-start.
func configuredExternalResolver() issueops.ExternalResolver {
	projects := make(map[string]string)
	for name := range config.GetExternalProjects() {
		projects[name] = config.ResolveExternalProjectPath(name)
	}
	return func(ctx context.Context, refs []string) (map[string]bool, error) {
		result := make(map[string]bool)
		byProject := make(map[string][]string)
		for _, ref := range refs {
			parts := strings.SplitN(ref, ":", 3)
			if len(parts) == 3 && parts[0] == "external" && parts[1] != "" && parts[2] != "" {
				byProject[parts[1]] = append(byProject[parts[1]], parts[2])
			}
		}
		for project, caps := range byProject {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			path := projects[project]
			if path == "" {
				continue
			}
			beadsDir := filepath.Join(path, ".beads")
			provider, err := newReadOnlyStoreFromConfig(ctx, beadsDir, true)
			if err != nil {
				continue
			}
			labels := make([]string, 0, len(caps))
			for _, cap := range caps {
				labels = append(labels, "provides:"+cap)
			}
			reader, readErr := provider.IssueReader()
			var page issueops.IssuePage
			if readErr == nil {
				limit := 0
				page, readErr = reader.List(ctx, issueops.ListRequest{Status: "closed", LabelsAny: labels, Limit: &limit,
					AllFlag: true, IncludeInfra: true, SkipCounts: true, Brief: true})
			}
			closeErr := provider.Close()
			if readErr != nil || closeErr != nil {
				continue
			}
			for _, row := range page.Items {
				for _, label := range row.Labels {
					if strings.HasPrefix(label, "provides:") {
						result["external:"+project+":"+strings.TrimPrefix(label, "provides:")] = true
					}
				}
			}
		}
		return result, ctx.Err()
	}
}
