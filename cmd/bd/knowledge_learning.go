package main

import (
	"github.com/spf13/cobra"
)

func init() {
	prepare := &cobra.Command{Use: "prepare <workspace-relative-source...>", Short: "Prepare a readonly session learning packet and editable draft for Codex", Args: cobra.RangeArgs(1, 16), RunE: func(cmd *cobra.Command, args []string) error {
		memories, reader, err := openKnowledge()
		if err != nil {
			return err
		}
		defer func() { _ = reader.Close() }()
		plane, err := memories.List(rootCtx, knowledgeMemorySelection())
		if err != nil {
			return err
		}
		session, _ := cmd.Flags().GetString("session")
		turn, _ := cmd.Flags().GetString("turn")
		ids, _ := cmd.Flags().GetStringArray("activity-event")
		packet, err := reader.PrepareLearning(plane.Memories, args, session, turn, ids)
		if err != nil {
			return err
		}
		return outputJSON(packet)
	}}
	prepare.Flags().String("session", "", "Observed session ID in this workspace; latest 16 events, omissions reported")
	prepare.Flags().String("turn", "", "Narrow the selected session to an observed turn")
	prepare.Flags().StringArray("activity-event", nil, "Explicit observed event ID; repeat up to 16 times instead of --session")
	knowledgeCmd.AddCommand(prepare)
}
