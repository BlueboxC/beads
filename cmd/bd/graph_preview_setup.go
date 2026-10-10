package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/cmd/bd/setup"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/templates/agents"
)

func runGraphPreviewSetup(cmd *cobra.Command, args []string) error {
	if err := graphPreviewFlags(cmd, "project", "check", "remove"); err != nil {
		return err
	}
	if len(args) != 1 || !strings.EqualFold(args[0], "claude") {
		return graphFailure("capability_unavailable", "graph setup supports only project-local setup claude [--project] [--check|--remove], without --json", 5)
	}
	for _, name := range []string{"project", "check", "remove"} {
		value, _ := cmd.Flags().GetBool(name)
		if cmd.Flags().Changed(name) && !value {
			return graphFailure("invalid_selector", "--"+name+" must be true when supplied", 2)
		}
	}
	check, _ := cmd.Flags().GetBool("check")
	remove, _ := cmd.Flags().GetBool("remove")
	if check && remove {
		return graphFailure("invalid_selector", "choose --check or --remove", 2)
	}
	if cmd.Flags().Changed("json") {
		return graphFailure("capability_unavailable", "graph Claude setup uses human-readable output; --json is not supported", 5)
	}
	if !check {
		if err := graphPreviewWritePolicy(); err != nil {
			return err
		}
	}
	workspace := filepath.Dir(graphPreviewDir)
	if !remove {
		// Read and validate, but do not install/refresh or replace any profile.
		plan, err := prepareGraphPreviewAgentInstructions(workspace, config.SafeAgentsFile(), false, graphPreviewSetupAgentsRemedy)
		if err != nil {
			return graphFailure("capability_unavailable", err.Error(), 5)
		}
		const begin = "<!-- BEGIN BEADS INTEGRATION"
		_, marker, present := strings.Cut(string(plan.before), begin)
		line, _, _ := strings.Cut(begin+marker, "\n")
		meta := agents.ParseMarker(line)
		if !present || meta == nil || meta.Profile != agents.ProfileGraphPreview {
			return graphFailure("capability_unavailable", "graph Claude setup requires existing graph-preview agent instructions; preserve existing instructions and initialize a fresh graph workspace without --skip-agents", 5)
		}
		if meta.Hash != agents.CurrentHash(agents.ProfileGraphPreview) {
			return graphFailure("capability_unavailable", "graph-preview instructions in "+config.SafeAgentsFile()+" have a stale managed hash; reconcile that managed block with the current graph guidance before setup (instructions and settings were preserved)", 5)
		}
	}
	if err := setup.GraphClaudeStop(workspace, check, remove); err != nil {
		return graphFailure("capability_unavailable", err.Error(), 5)
	}
	return nil
}

// Claude Code reads exit 2 from a hook as "block stopping" and feeds stderr
// back to the agent, and admission runs before Steph's stop_hook_active guard,
// so an invalid_selector refusal (such as BD_BACKEND) could loop. Replace any
// admission refusal with one warning line and exit 1, as an ordinary
// workspace exits on BD_BACKEND; Claude Code treats exit 1 as non-blocking.
func graphClaudeHookAdmissionWarning(diagnostic string, err error) error {
	reason := strings.Join(strings.Fields(diagnostic), " ")
	if reason == "" {
		reason = strings.Join(strings.Fields(err.Error()), " ")
	}
	fmt.Fprintf(os.Stderr, "Warning: bd claude-hook skipped; graph admission refused it: %s\n", reason) //nolint:gosec // G705: stderr, not a browser context
	return &exitError{Code: 1}
}

func admitGraphPreviewClaudeStop(cmd *cobra.Command, args []string) error {
	if err := graphPreviewFlags(cmd); err != nil {
		return err
	}
	if len(args) != 1 || !strings.EqualFold(args[0], "stop") || cmd.Flags().Changed("json") {
		return graphFailure("capability_unavailable", "graph claude-hook supports only stop with its native JSON stdin/stdout protocol", 5)
	}
	return nil
}
