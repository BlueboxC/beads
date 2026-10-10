package main

import (
	"crypto/rand"
	"encoding/hex"
	"strings"

	"github.com/spf13/cobra"
	graph "github.com/steveyegge/beads/graphops"
)

// Allocate once, then let the existing writer reserve the identity atomically.
// Explicit IDs (including explicitly empty ones) are never replaced or retried.
// The 128-bit random suffix follows graph workspace/token allocation; it avoids
// using the Issue hash generator's title/time/nonce collision protocol here.
func graphPreviewCreateBeadPath(cmd *cobra.Command) (string, error) {
	path, _ := cmd.Flags().GetString("id")
	if !cmd.Flags().Changed("id") {
		var token [16]byte
		if _, err := rand.Read(token[:]); err != nil {
			return "", err
		}
		path = "beads/" + hex.EncodeToString(token[:])
	} else if graph.ValidateBeadPath(path) != nil {
		var err error
		path, err = graphPreviewBareBeadPath(path)
		if err != nil {
			return "", graphFailure("invalid_selector", err.Error(), 2)
		}
	}
	if err := graph.ValidateBeadPath(path); err != nil {
		return "", graphFailure("invalid_selector", err.Error(), 2)
	}
	return path, nil
}

// This is a deterministic display summary, not an inferred semantic title.
// The complete body remains unchanged. Whitespace-only bodies get an empty title.
func graphPreviewMemoryTitle(body string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(body), "\n")
	title := []rune(strings.Join(strings.Fields(line), " "))
	const maxRunes = 80
	if len(title) > maxRunes {
		return string(title[:maxRunes-1]) + "…"
	}
	return string(title)
}
