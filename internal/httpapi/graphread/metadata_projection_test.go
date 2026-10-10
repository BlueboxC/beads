package graphread

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi/bdpwire"
)

func TestCurrentResourceMetadataProjection(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, json.RawMessage(`{}`)} {
		metadata, err := projectMetadata(raw)
		if err != nil || metadata == nil {
			t.Fatalf("empty metadata projection = %#v, %v", metadata, err)
		}
		record := bdpwire.BeadRecord{ID: "https://example.test/beads/x", Type: "https://example.test/types/x", Revision: "r", Properties: bdpwire.Properties{}, Metadata: metadata}
		encoded, err := json.Marshal(record)
		if err != nil || !strings.Contains(string(encoded), `"metadata":{}`) {
			t.Fatalf("current record omitted empty metadata: %s, %v", encoded, err)
		}
	}
	metadata, err := projectMetadata(json.RawMessage(`{"reviewed":true}`))
	if err != nil || string(metadata["reviewed"]) != "true" {
		t.Fatalf("nonempty metadata projection = %#v, %v", metadata, err)
	}
	if _, err := projectMetadata(json.RawMessage(`[]`)); err == nil {
		t.Fatal("non-object metadata reached the public projection")
	}
}
