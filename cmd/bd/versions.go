package main

import (
	"fmt"
	"github.com/spf13/cobra"
)

var versionsCmd = &cobra.Command{
	Use: "versions <id>", GroupID: "views", Short: "List retained versions in a Graph Preview workspace",
	Long: "List the versions recorded for a bead by versioned history. This fork exposes retained versions only in Graph Preview; ordinary issues use bd history.",
	Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if graphPreviewActive {
			return runGraphPreviewVersions(cmd, args)
		}
		return fmt.Errorf("retained versions require a Graph Preview workspace; use bd history for ordinary issue history")
	}}

func init() { rootCmd.AddCommand(versionsCmd) }
