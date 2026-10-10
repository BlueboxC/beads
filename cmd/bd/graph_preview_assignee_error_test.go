package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

func TestGraphPreviewAssigneeOwnershipError(t *testing.T) {
	stderr, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	oldStderr, oldJSON := os.Stderr, jsonOutput
	os.Stderr, jsonOutput = stderr, true
	t.Cleanup(func() {
		os.Stderr, jsonOutput = oldStderr, oldJSON
		_ = stderr.Close()
	})
	result := graphStorageError(fmt.Errorf("active assignment: %w", storage.ErrAlreadyClaimed))
	var failure *exitError
	if !errors.As(result, &failure) || failure.Code != 4 {
		t.Fatalf("ownership refusal must exit4: %v", result)
	}
	if _, err := stderr.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		Retryable bool   `json:"retryable"`
	}
	if err := json.NewDecoder(stderr).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Code != "constraint_violation" || envelope.Retryable || envelope.Message != "active assignment: "+storage.ErrAlreadyClaimed.Error() {
		t.Fatalf("ownership refusal misclassified: %+v", envelope)
	}
}
