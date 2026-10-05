package main

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/codeindex"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/ui"
)

func init() {
	impact := &cobra.Command{Use: "impact <path-or-exact-symbol>", Short: "Trace inverse static dependencies, preserved learnings and candidate tests", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, reader, plane, index, err := openCodeIndex()
			if err != nil {
				return err
			}
			defer func() { _ = reader.Close() }()
			index = reader.Refresh(index)
			state := reader.Knowledge(plane)
			depth, _ := cmd.Flags().GetInt("depth")
			limit, _ := cmd.Flags().GetInt("limit")
			report, err := codeindex.Impact(index, state, args[0], depth, limit)
			if err != nil {
				return err
			}
			if jsonOutput {
				return outputJSON(report)
			}
			fmt.Println("Conservative static file impact; candidate tests do not prove coverage.")
			for _, file := range report.Files {
				fmt.Printf("%s depth=%d [%s]\n", ui.SanitizeForTerminal(file.Path), file.Depth, file.Validity)
			}
			for _, test := range report.Tests {
				fmt.Printf("Test candidate %s (%s)\n", ui.SanitizeForTerminal(test.Path), test.Reason)
			}
			for _, record := range report.Knowledge {
				fmt.Printf("Learning %s [%s; %s]: %s\n", ui.SanitizeForTerminal(record.ID), record.Scope, record.Validity, ui.SanitizeForTerminal(record.Summary))
			}
			fmt.Printf("Omitted files: %d; depth limited: %t; unresolved references in index: %d. Use --json for witness paths and evidence.\n", report.Omitted, report.DepthLimited, report.UnresolvedTotal)
			return nil
		}}
	impact.Flags().Int("depth", 8, "Maximum inverse dependency depth, 1-32")
	impact.Flags().Int("limit", 200, "Maximum displayed affected files, 1-1024; full traversal counts omissions")
	relink := &cobra.Command{Use: "relink <old-path> <new-path>", Short: "Prepare readonly learning drafts for an exact-content move; never renew evidence", Args: cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			_, reader, plane, index, err := openCodeIndex()
			if err != nil {
				return err
			}
			defer func() { _ = reader.Close() }()
			report, err := reader.PrepareRelink(reader.Refresh(index), reader.Knowledge(plane), args[0], args[1])
			if err != nil {
				return err
			}
			return outputJSON(report)
		}}
	prune := &cobra.Command{Use: "prune", Short: "Plan obsolete code-blob cleanup; --apply requires atomic embedded memory publication", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			apply, _ := cmd.Flags().GetBool("apply")
			if apply {
				CheckReadonly("code prune --apply")
				cfg, err := configfile.Load(beads.FindBeadsDir())
				if err != nil {
					return err
				}
				if cfg == nil || cfg.GetBackend() != configfile.BackendDolt || cfg.IsDoltServerMode() || cfg.IsDoltProxiedServerMode() || usesProxiedServer() {
					return errors.New("prune --apply supports the upgraded embedded writer only; shared/server or mixed old writers are not qualified")
				}
			}
			memories, reader, plane, _, err := openCodeIndex()
			if err != nil {
				return err
			}
			defer func() { _ = reader.Close() }()
			plan, err := codeindex.PlanPrune(plane)
			if apply {
				plan, err = codeindex.ApplyPrune(rootCtx, memories, plane)
				if err == nil && plan.Deleted > 0 {
					noteDirectMemoryWrite()
				}
			}
			if err != nil {
				return err
			}
			return outputJSON(plan)
		}}
	prune.Flags().Bool("apply", false, "Explicitly delete only obsolete derived blobs atomically; retain Dolt history and human knowledge")
	codeCmd.AddCommand(impact, relink, prune)
}
