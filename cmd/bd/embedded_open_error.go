package main

import (
	"encoding/json"
	"os"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

func handleEmbeddedOpenPermissionJSON(e *embeddeddolt.OpenPermissionError) {
	outer := buildJSONError(e.Error(), e.Hint())
	if m, ok := outer.(map[string]interface{}); ok {
		m["code"] = "embedded_open.permission_denied"
		m["retryable"] = false
	}
	encoder := json.NewEncoder(os.Stderr)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(outer)
}
