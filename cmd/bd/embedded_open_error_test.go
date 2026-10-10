package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

func TestEmbeddedOpenPermissionRendering(t *testing.T) {
	oldJSON := jsonOutput
	t.Cleanup(func() { jsonOutput = oldJSON })
	denied := &embeddeddolt.OpenPermissionError{Err: fmt.Errorf("open LOCK: %w", os.ErrPermission)}
	wrapped := fmt.Errorf("open provider: %w", denied)
	jsonOutput = false
	text := captureStderr(t, func() {
		if !renderTypedOpenError(wrapped) {
			t.Fatal("did not render typed permission error")
		}
	})
	if !strings.Contains(text, "filesystem write access") || !strings.Contains(text, "Retrying with unchanged") {
		t.Fatalf("missing permanent permission guidance: %s", text)
	}
	jsonOutput = true
	output := captureStderr(t, func() { renderTypedOpenError(wrapped) })
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(output), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["code"] != "embedded_open.permission_denied" || parsed["retryable"] != false ||
		parsed["schema_version"] != float64(JSONSchemaVersion) || parsed["error"] != denied.Error() || parsed["hint"] != denied.Hint() {
		t.Fatalf("invalid machine-readable failure: %v", parsed)
	}
	if primeMemoryFailureReason(wrapped) != "permission_denied" {
		t.Fatal("lost hook failure category")
	}
}
