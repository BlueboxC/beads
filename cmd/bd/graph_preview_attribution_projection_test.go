package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/graphstore"
)

func TestGraphPreviewAttributionBasisPreservesStoredStatusAndUserProperties(t *testing.T) {
	record := graphstore.Record{
		ID:       "https://example.test/beads/one",
		Type:     "https://example.test/types/memory",
		Version:  "v1",
		Revision: "v1",
		Properties: graphstore.Properties{
			Title: "One",
			Body:  `{"attribution":{"status":"claimed"}}`,
		},
		Attribution: graphstore.Attribution{Actor: "agent:writer", Status: "claimed", RecordedAt: "2026-10-09T00:00:00Z"},
	}
	stored, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stored), `"status":"claimed"`) {
		t.Fatalf("stored snapshot unexpectedly changed: %s", stored)
	}
	projected, err := graphProjectCompleteRecords(record)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(projected, &result); err != nil {
		t.Fatal(err)
	}
	attribution := result["attribution"].(map[string]any)
	if attribution["actor"] != "agent:writer" || attribution["basis"] != "writer-supplied" {
		t.Fatalf("wrong public attribution: %v", attribution)
	}
	if _, old := attribution["status"]; old {
		t.Fatalf("old status leaked into public output: %v", attribution)
	}
	if record.Attribution.Status != "claimed" || result["properties"].(map[string]any)["body"] != record.Properties.Body {
		t.Fatal("projection changed stored state or user content")
	}
}

func TestGraphPreviewAbsentAttributionHasNoPrincipal(t *testing.T) {
	for _, attribution := range []any{graphstore.Attribution{Status: "unknown"}, graphstore.Attribution{}, nil} {
		projected, err := graphProjectCompleteRecords(map[string]any{
			"attribution": attribution,
			"properties":  map[string]any{"attribution": map[string]any{"status": "claimed"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if err := json.Unmarshal(projected, &result); err != nil {
			t.Fatal(err)
		}
		if _, present := result["attribution"]; present {
			t.Fatalf("principal-free attribution should be absent: %s", projected)
		}
		if result["properties"].(map[string]any)["attribution"].(map[string]any)["status"] != "claimed" {
			t.Fatalf("user property was transformed: %s", projected)
		}
	}
}

func TestGraphPreviewNativeIssueVersionAttributionLabelUnchanged(t *testing.T) {
	projected, err := graphProjectCompleteRecords(map[string]any{"attribution": "native-issue-writer"})
	if err != nil {
		t.Fatal(err)
	}
	if string(projected) != `{"attribution":"native-issue-writer"}` {
		t.Fatalf("native Issue version attribution changed: %s", projected)
	}
}

func TestGraphPreviewInvalidStoredAttributionRefusesAsInvalidStore(t *testing.T) {
	for _, status := range []string{"verified", ""} {
		_, _, err := graphProjectCarriedAttribution(json.RawMessage(fmt.Sprintf(`{"actor":"agent:writer","status":%q}`, status)))
		if !errors.Is(err, graphstore.ErrInvalidStore) {
			t.Fatalf("status %q should be invalid store: %v", status, err)
		}
	}
	_, _, err := graphProjectCarriedAttribution(json.RawMessage(`{"actor":"","status":"claimed"}`))
	if !errors.Is(err, graphstore.ErrInvalidStore) {
		t.Fatalf("principal-free claimed attribution should be invalid store: %v", err)
	}
	for _, raw := range []string{`{"actor":123,"status":"claimed"}`, `{"actor":"agent:writer"}`} {
		if _, _, err := graphProjectCarriedAttribution(json.RawMessage(raw)); !errors.Is(err, graphstore.ErrInvalidStore) {
			t.Fatalf("malformed stored attribution %s should be invalid store: %v", raw, err)
		}
	}
	if _, err := graphPublicAttributionBasis("verified"); !errors.Is(err, graphstore.ErrInvalidStore) {
		t.Fatalf("human attribution should fail closed: %v", err)
	}

	var diagnostic bytes.Buffer
	oldOutput, oldJSON := graphFailureOutput, jsonOutput
	graphFailureOutput, jsonOutput = &diagnostic, true
	t.Cleanup(func() {
		graphFailureOutput, jsonOutput = oldOutput, oldJSON
	})
	result := graphStorageError(fmt.Errorf("projection: %w", graphstore.ErrInvalidStore))
	var failure *exitError
	if !errors.As(result, &failure) || failure.Code != 5 {
		t.Fatalf("invalid store must exit5: %v", result)
	}
	var envelope struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(diagnostic.Bytes(), &envelope); err != nil || envelope.Code != "invalid_store" {
		t.Fatalf("invalid store must use its own code: %+v, %v", envelope, err)
	}
}

func TestGraphPreviewReplacementDisclosureUsesPublicBasis(t *testing.T) {
	replaced := &graphstore.ReplacedMemory{ID: "beads/plan", Version: "saved", Attribution: graphstore.Attribution{Actor: "agent:writer", Status: "claimed"}}
	human, err := graphPreviewReplacementSummary("Updated", replaced)
	if err != nil || !strings.Contains(human, `basis="writer-supplied"`) || strings.Contains(human, `status="claimed"`) {
		t.Fatalf("replacement disclosure: %q, %v", human, err)
	}
	replaced.Attribution = graphstore.Attribution{Status: "unknown"}
	human, err = graphPreviewReplacementSummary("Updated", replaced)
	if err != nil || !strings.Contains(human, "no recorded attribution") {
		t.Fatalf("absent attribution disclosure: %q, %v", human, err)
	}
	replaced.Attribution = graphstore.Attribution{Status: "claimed"}
	if _, err := graphPreviewReplacementSummary("Updated", replaced); !errors.Is(err, graphstore.ErrInvalidStore) {
		t.Fatalf("actorless claimed attribution should fail closed: %v", err)
	}
	replaced.Attribution = graphstore.Attribution{Actor: "agent:writer", Status: "verified"}
	if _, err := graphPreviewReplacementSummary("Updated", replaced); !errors.Is(err, graphstore.ErrInvalidStore) {
		t.Fatalf("out-of-vocabulary disclosure should fail closed: %v", err)
	}
}
