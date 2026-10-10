package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	graph "github.com/steveyegge/beads/graphops"
	"github.com/steveyegge/beads/internal/storage/graphstore"
)

type graphTypeInventory struct {
	BeadTypes []json.RawMessage `json:"beadTypes"`
	LinkTypes []json.RawMessage `json:"linkTypes"`
}

// The source of this inventory is the selected workspace's persisted Type
// installation. A binary with six known Types may open an older four-Type store.
func runGraphPreviewTypes(cmd *cobra.Command, args []string) error {
	if err := graphPreviewFlags(cmd, "details"); err != nil {
		return err
	}
	if len(args) != 0 {
		return graphFailure("invalid_selector", "bd types takes no arguments", 2)
	}
	details, _ := cmd.Flags().GetBool("details")
	return withGraphStore(func(ctx context.Context, store *graphstore.Store) (any, string, error) {
		descriptors, err := store.ListInstalledTypes(ctx)
		if err != nil {
			return nil, "", err
		}
		inventory := graphTypeInventory{
			BeadTypes: []json.RawMessage{}, LinkTypes: []json.RawMessage{},
		}
		var beadText, linkText strings.Builder
		for _, descriptor := range descriptors {
			id := strings.TrimPrefix(descriptor.ID(), store.ScopeURL())
			var destination *strings.Builder
			switch descriptor.Describes() {
			case graph.KindBead:
				inventory.BeadTypes = append(inventory.BeadTypes, json.RawMessage(descriptor.CanonicalJSON()))
				destination = &beadText
			case graph.KindLink:
				inventory.LinkTypes = append(inventory.LinkTypes, json.RawMessage(descriptor.CanonicalJSON()))
				destination = &linkText
			default:
				return nil, "", fmt.Errorf("%w: installed Type has unknown resource category", graphstore.ErrInvalidStore)
			}
			fmt.Fprintf(destination, "  %-30s %s\n", graphMemoryDisplayText(id), graphMemoryDisplayText(descriptor.Name()))
			if details {
				var pretty bytes.Buffer
				if err := json.Indent(&pretty, descriptor.CanonicalJSON(), "    ", "  "); err != nil {
					return nil, "", err
				}
				for _, line := range strings.Split(pretty.String(), "\n") {
					fmt.Fprintf(destination, "    %s\n", line)
				}
			}
		}
		human := fmt.Sprintf("Bead Types (%d):\n%s\nLink Types (%d):\n%s\nUse these types/NAME IDs with --bead-type or --link-type.",
			len(inventory.BeadTypes), beadText.String(), len(inventory.LinkTypes), linkText.String())
		return inventory, human, nil
	})
}
