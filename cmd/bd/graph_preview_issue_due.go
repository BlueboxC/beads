package main

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/timeparsing"
)

// Empty is absent on create and an explicit clear on update. The caller owns
// presence; storage owns the existing DATETIME representation, not this parser.
// The 4096-byte bound is provisional private-preview admission, not a public
// date-language or BDP limit.
func graphPreviewIssueDueInput(cmd *cobra.Command) (*time.Time, error) {
	value, err := cmd.Flags().GetString("due")
	if err != nil {
		return nil, graphFailure("invalid_properties", err.Error(), 2)
	}
	if !utf8.ValidString(value) || len(value) > 4096 {
		return nil, graphFailure("invalid_properties", "--due must be UTF-8 and at most 4096 bytes", 2)
	}
	if value == "" {
		return nil, nil
	}
	// Keep ordinary "m" as months; Graph Preview also accepts explicit minutes.
	if strings.HasSuffix(value, "min") {
		duration, parseErr := time.ParseDuration(strings.TrimSuffix(value, "min") + "m")
		if parseErr != nil {
			return nil, graphFailure("invalid_properties", parseErr.Error(), 2)
		}
		parsed := time.Now().Add(duration).UTC()
		return &parsed, nil
	}
	parsed, err := timeparsing.ParseRelativeTime(value, time.Now())
	if err != nil {
		return nil, graphFailure("invalid_properties", err.Error(), 2)
	}
	return &parsed, nil
}
