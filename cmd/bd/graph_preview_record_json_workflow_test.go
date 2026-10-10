//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/steveyegge/beads/internal/storage/graphstore"
)

func graphAssertCompleteRecordRevisionOnly(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var record map[string]json.RawMessage
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	if _, exists := record["version"]; exists {
		t.Fatalf("complete CLI record retained duplicate version: %s", raw)
	}
	var revision string
	if err := json.Unmarshal(record["revision"], &revision); err != nil || revision == "" {
		t.Fatalf("complete CLI record lost revision: %s (%v)", raw, err)
	}
	return revision
}

func TestGraphPreviewRecordJSONWorkflow(t *testing.T) {
	bd := buildBDUnderTest(t)
	for _, engine := range []string{"embedded", "server"} {
		t.Run(engine, func(t *testing.T) {
			work, home := t.TempDir(), t.TempDir()
			const scope = "https://example.invalid/record-json/"
			args := []string{"init", "--graph-mode", "link", "--scope-url", scope, "--skip-hooks", "--skip-agents", "--non-interactive"}
			if engine == "server" {
				port := os.Getenv("BEADS_GRAPH_TEST_SERVER_PORT")
				if port == "" {
					t.Skip("set BEADS_GRAPH_TEST_SERVER_PORT for ordinary shared-server JSON qualification")
				}
				args = append(args, "--server", "--external", "--server-host", "127.0.0.1", "--server-port", port, "--server-user", "root")
			}
			call := func(args ...string) json.RawMessage {
				t.Helper()
				return graphMixedResult[json.RawMessage](t, graphPolicyCLI(t, bd, work, home, nil, "", append(args, "--json")...))
			}
			call(args...)
			memory := call("remember", "Private body", "--id", "beads/plan", "--title", "Plan")
			memoryRevision := graphAssertCompleteRecordRevisionOnly(t, memory)
			graphAssertCompleteRecordRevisionOnly(t, call("create", "Work", "--id", "beads/work"))
			var receipt struct {
				Link   json.RawMessage `json:"link"`
				Source json.RawMessage `json:"source"`
			}
			if err := json.Unmarshal(call("link", "beads/plan", "beads/work", "--link-type", "types/preview-related-v2", "--properties", `{"note":"user value"}`), &receipt); err != nil {
				t.Fatal(err)
			}
			graphAssertCompleteRecordRevisionOnly(t, receipt.Link)
			graphAssertCompleteRecordRevisionOnly(t, receipt.Source)
			var linked graphstore.Record
			if err := json.Unmarshal(call("show", "beads/plan"), &linked); err != nil || len(linked.Owned) != 1 {
				t.Fatalf("owner did not expose its complete owned Link: %+v (%v)", linked, err)
			}
			graphAssertCompleteRecordRevisionOnly(t, linked.Owned[0])
			var link map[string]json.RawMessage
			if err := json.Unmarshal(receipt.Link, &link); err != nil {
				t.Fatal(err)
			}
			if string(link["properties"]) != `{"note":"user value"}` {
				t.Fatalf("caller-authored Link property changed: %s", receipt.Link)
			}
			graphAssertCompleteRecordRevisionOnly(t, call("show", "beads/plan", "--version", memoryRevision))
		})
	}
}
