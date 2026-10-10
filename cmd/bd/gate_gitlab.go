package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/types"
)

type gitLabGateRunner func(context.Context, ...string) ([]byte, error)

func runGitLabGateCommand(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "glab", args...) // #nosec G204 -- validated argument vector, no shell
	cmd.WaitDelay = time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("glab query failed: %w", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(stdout, 1024*1024+1))
	if readErr != nil || len(data) > 1024*1024 {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("GitLab response unreadable or exceeds 1 MiB")
	}
	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("glab query failed: %w", err)
	}
	return data, nil
}

// An absent selector delegates repository and authenticated host discovery to glab.
// Explicit selectors are project paths, including nested groups, never host guesses.
func gitLabProjectFromIssue(issue *types.Issue) (string, error) {
	if issue == nil || len(issue.Metadata) == 0 || string(issue.Metadata) == "null" {
		return "", nil
	}
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(issue.Metadata, &metadata); err != nil {
		return "", fmt.Errorf("metadata must be a JSON object: %w", err)
	}
	raw, exists := metadata["repo"]
	if !exists {
		return "", nil
	}
	var project *string
	if err := json.Unmarshal(raw, &project); err != nil || project == nil {
		return "", fmt.Errorf("metadata.repo must be a string")
	}
	if *project == "" {
		return "", nil
	}
	parts := strings.Split(*project, "/")
	if len(parts) < 2 || len(*project) > 512 {
		return "", fmt.Errorf("metadata.repo must be a group/project path")
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.HasPrefix(part, "-") {
			return "", fmt.Errorf("metadata.repo contains an invalid path component")
		}
		for _, char := range part {
			if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-' || char == '_' || char == '.') {
				return "", fmt.Errorf("metadata.repo contains an invalid character")
			}
		}
	}
	return *project, nil
}

func gitLabProjectEndpoint(project string) string {
	if project == "" {
		return "projects/:fullpath"
	}
	return "projects/" + url.PathEscape(project)
}

func gitLabGateQuery(ctx context.Context, run gitLabGateRunner, endpoint string, result interface{}) error {
	data, err := run(ctx, "api", "--method", "GET", "--output", "json", endpoint)
	if err != nil {
		return err
	}
	if len(data) > 1024*1024 {
		return fmt.Errorf("GitLab response exceeds 1 MiB")
	}
	if err := json.Unmarshal(data, result); err != nil {
		return fmt.Errorf("invalid GitLab response: %w", err)
	}
	return nil
}

func gitLabPositiveID(value string) (int64, error) {
	if !isNumericID(value) {
		return 0, fmt.Errorf("await_id must be a positive numeric ID")
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("await_id must be a positive numeric ID")
	}
	return id, nil
}

func checkGitLabGate(ctx context.Context, gate *types.Issue) (bool, bool, string, error) {
	return checkGitLabGateWithRunner(ctx, gate, runGitLabGateCommand)
}

func checkGitLabGateWithRunner(ctx context.Context, gate *types.Issue, run gitLabGateRunner) (bool, bool, string, error) {
	if gate.AwaitID == "" {
		return false, false, "no GitLab ID specified; discover a pipeline or set await_id", nil
	}
	id, err := gitLabPositiveID(gate.AwaitID)
	if err != nil {
		return false, false, "", err
	}
	project, err := gitLabProjectFromIssue(gate)
	if err != nil {
		return false, false, "", err
	}
	endpoint := gitLabProjectEndpoint(project)
	switch gate.AwaitType {
	case "gl:pipeline":
		var pipeline struct {
			ID     int64  `json:"id"`
			Status string `json:"status"`
		}
		if err := gitLabGateQuery(ctx, run, endpoint+"/pipelines/"+strconv.FormatInt(id, 10), &pipeline); err != nil {
			return false, false, "", err
		}
		if pipeline.ID != id || pipeline.Status == "" {
			return false, false, "", fmt.Errorf("GitLab pipeline response is missing status or has the wrong ID")
		}
		switch pipeline.Status {
		case "success":
			return true, false, "GitLab pipeline succeeded", nil
		case "failed", "canceled":
			return false, true, "GitLab pipeline failed or was canceled", nil
		default:
			return false, false, "GitLab pipeline has not succeeded", nil
		}
	case "gl:mr":
		var mr struct {
			IID   int64  `json:"iid"`
			State string `json:"state"`
		}
		if err := gitLabGateQuery(ctx, run, endpoint+"/merge_requests/"+strconv.FormatInt(id, 10), &mr); err != nil {
			return false, false, "", err
		}
		if mr.IID != id || mr.State == "" {
			return false, false, "", fmt.Errorf("GitLab MR response is missing state or has the wrong IID")
		}
		switch mr.State {
		case "merged":
			return true, false, "GitLab merge request was merged", nil
		case "closed":
			return false, true, "GitLab merge request was closed without merging", nil
		default:
			return false, false, "GitLab merge request has not merged", nil
		}
	default:
		return false, false, "", fmt.Errorf("unsupported GitLab gate type")
	}
}

// Discovery pins a single pipeline for the current checkout's exact branch and HEAD.
// An explicit foreign project is refused rather than borrowing local Git evidence.
func discoverGitLabPipeline(ctx context.Context, gate *types.Issue, branch, sha string, run gitLabGateRunner) (string, error) {
	if branch == "" || branch == "HEAD" || sha == "" {
		return "", fmt.Errorf("pipeline discovery requires a current branch and HEAD")
	}
	project, err := gitLabProjectFromIssue(gate)
	if err != nil {
		return "", err
	}
	if project != "" {
		var current struct {
			Path string `json:"path_with_namespace"`
		}
		if err := gitLabGateQuery(ctx, run, "projects/:fullpath", &current); err != nil {
			return "", err
		}
		if current.Path != project {
			return "", fmt.Errorf("foreign project discovery requires an explicit pipeline ID")
		}
	}
	query := url.Values{"ref": {branch}, "sha": {sha}, "order_by": {"id"}, "sort": {"desc"}, "per_page": {"100"}}
	var pipelines []struct {
		ID  int64  `json:"id"`
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	}
	if err := gitLabGateQuery(ctx, run, gitLabProjectEndpoint(project)+"/pipelines?"+query.Encode(), &pipelines); err != nil {
		return "", err
	}
	if len(pipelines) > 100 {
		return "", fmt.Errorf("GitLab returned more than 100 pipeline candidates")
	}
	var latest int64
	for _, pipeline := range pipelines {
		if pipeline.Ref == branch && pipeline.SHA == sha && pipeline.ID > latest {
			latest = pipeline.ID
		}
	}
	if latest == 0 {
		return "", fmt.Errorf("no pipeline matches the current branch and HEAD")
	}
	return strconv.FormatInt(latest, 10), nil
}

func runGitLabGateDiscovery(cmd *cobra.Command) error {
	branch, _ := cmd.Flags().GetString("branch")
	ctx, cancel := context.WithTimeout(rootCtx, 30*time.Second)
	defer cancel()
	repo, err := beads.GetRepoContext()
	if err != nil {
		return err
	}
	current, err := repo.GitCmdCWD(ctx, "symbolic-ref", "--quiet", "--short", "HEAD").Output()
	if err != nil {
		return fmt.Errorf("pipeline discovery requires a current Git branch: %w", err)
	}
	currentBranch := strings.TrimSpace(string(current))
	if branch == "" {
		branch = currentBranch
	}
	if branch != currentBranch {
		return fmt.Errorf("pipeline discovery requires the current branch; set a pipeline ID for another branch")
	}
	head, err := repo.GitCmdCWD(ctx, "rev-parse", "HEAD").Output()
	if err != nil {
		return err
	}
	sha := strings.TrimSpace(string(head))
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	gates, err := findUnclosedGates(rootCtx)
	if err != nil {
		return err
	}
	var failures, matched int
	for _, gate := range gates {
		if gate.AwaitType != "gl:pipeline" || gate.AwaitID != "" {
			continue
		}
		id, err := discoverGitLabPipeline(rootCtx, gate, branch, sha, runGitLabGateCommand)
		if err == nil && !dryRun {
			err = updateGateAwaitID(rootCtx, gate.ID, id)
		}
		if err != nil {
			failures++
			fmt.Fprintf(cmd.ErrOrStderr(), "%s: %v\n", gate.ID, err)
			continue
		}
		matched++
		fmt.Fprintf(cmd.OutOrStdout(), "%s: pipeline %s (dry-run=%t)\n", gate.ID, id, dryRun)
	}
	if failures > 0 {
		return fmt.Errorf("GitLab discovery: %d matched, %d unresolved", matched, failures)
	}
	return nil
}
