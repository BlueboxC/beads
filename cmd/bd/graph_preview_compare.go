package main

import (
	"context"
	"encoding/json"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage/graphstore"
)

var graphCompareCmd = &cobra.Command{
	Use:   "compare RESOURCE --from TOKEN --to TOKEN",
	Short: "Compare two exact retained graph versions (experimental)",
	Long: `Compare complete retained preview properties and owned Links for one Resource.
An unprefixed selector means a Bead under beads/; use links/PATH for a Link.
The explicit from/to tokens select direction, not a chronology. Both versions must
be available. Output is experimental JSON (indented for humans); common metadata,
full Memory fields and public History are not implemented. Use bd diff for Dolt refs.`,
	Args:          cobra.ExactArgs(1),
	GroupID:       "views",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runGraphPreviewCompare,
}

func init() {
	graphCompareCmd.Flags().String("from", "", "Required exact retained source version token")
	graphCompareCmd.Flags().String("to", "", "Required exact retained destination version token")
	rootCmd.AddCommand(graphCompareCmd)
}

func runGraphPreviewCompare(cmd *cobra.Command, args []string) error {
	if err := graphPreviewFlags(cmd, "from", "to"); err != nil {
		return err
	}
	path, err := graphPreviewResourcePath(graphPreviewConfig.GraphScopeURL, args[0])
	if err != nil {
		return graphFailure("invalid_selector", err.Error(), 2)
	}
	from, _ := cmd.Flags().GetString("from")
	to, _ := cmd.Flags().GetString("to")
	for _, token := range []string{from, to} {
		if token == "" || !utf8.ValidString(token) || len(token) > graphstore.PreviewVersionTokenLimit {
			return graphFailure("invalid_selector", "--from and --to require nonempty UTF-8 tokens of at most 4096 bytes each", 2)
		}
	}
	return withGraphStore(func(ctx context.Context, store *graphstore.Store) (any, string, error) {
		result, err := store.CompareVersions(ctx, path, from, to)
		if err != nil {
			return nil, "", err
		}
		projected, err := graphProjectCompleteRecords(result)
		if err != nil {
			return nil, "", err
		}
		human, err := json.MarshalIndent(projected, "", "  ")
		return result, string(human), err
	})
}
