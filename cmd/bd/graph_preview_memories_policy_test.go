//go:build cgo

package main

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/graphstore"
)

// Separate real CLI processes exercise configuration loading and local flag
// shadowing. In-process flag mutation cannot reproduce either regression.
func TestGraphPreviewMemoryDiscoveryConfiguredJSONFormatPrecedence(t *testing.T) {
	bd := buildBDUnderTest(t)
	work, home := t.TempDir(), t.TempDir()
	const scope = "https://example.invalid/discovery-format/"
	const body = "PRIVATE_BODY_DO_NOT_PROJECT — 雪\n"
	graphPolicyCLI(t, bd, work, home, nil, "", "init", "--graph-mode", "link", "--scope-url", scope, "--prefix", "dfmt", "--skip-hooks", "--skip-agents", "--non-interactive", "--json")
	created := graphPolicyCLI(t, bd, work, home, nil, "", "remember", body, "--id", "beads/plan", "--title", "Format plan", "--json")
	var saved struct {
		Result graphstore.Record `json:"result"`
	}
	if err := json.Unmarshal([]byte(created), &saved); err != nil || saved.Result.ID != scope+"beads/plan" || saved.Result.Revision == "" {
		t.Fatalf("remember receipt: %s (%v)", created, err)
	}
	writeFile(t, filepath.Join(work, ".beads", "config.yaml"), []byte("json: true\n"))
	t.Run("explicit-table", func(t *testing.T) {
		out := graphPolicyCLI(t, bd, work, home, nil, "", "memories", "--format", "table")
		if !strings.HasPrefix(out, "Memories (1):\n") || json.Valid([]byte(out)) || !strings.Contains(out, "beads/plan  Format plan") || strings.Contains(out, saved.Result.ID) || strings.Contains(out, saved.Result.Revision) || !strings.Contains(out, "bd recall") || strings.Contains(out, body) {
			t.Fatalf("configured JSON overrode or corrupted table: %q", out)
		}
	})
	t.Run("explicit-records-json", func(t *testing.T) {
		out := graphPolicyCLI(t, bd, work, home, nil, "", "memories", "--format", "records-json")
		var envelope struct {
			SchemaVersion int                        `json:"schemaVersion"`
			Preview       bool                       `json:"preview"`
			Result        graphMemoryDiscoveryResult `json:"result"`
		}
		if err := json.Unmarshal([]byte(out), &envelope); err != nil {
			t.Fatalf("records JSON: %v\n%s", err, out)
		}
		result := envelope.Result
		if envelope.SchemaVersion != 1 || !envelope.Preview || result.Projection != "summary" || result.Scope != scope || !result.Complete || result.Next != nil || len(result.Items) != 1 {
			t.Fatalf("wrong summary envelope: %s", out)
		}
		item := result.Items[0]
		if item.ID != saved.Result.ID || item.Version != saved.Result.Revision || item.Title != saved.Result.Properties.Title || item.Attribution != saved.Result.Attribution || item.MatchedFields == nil || len(item.MatchedFields) != 0 || item.Excerpt != nil || strings.Contains(out, "PRIVATE_BODY_DO_NOT_PROJECT") {
			t.Fatalf("wrong summary record: %s", out)
		}
	})
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"configured-json-without-format", []string{"memories"}},
		{"explicit-json", []string{"memories", "--json"}},
		{"table-plus-explicit-json", []string{"memories", "--format", "table", "--json"}},
		{"records-plus-explicit-json", []string{"memories", "--format", "records-json", "--json"}},
	} {
		t.Run(tc.name, func(t *testing.T) { graphPolicyCLI(t, bd, work, home, nil, "capability_unavailable", tc.args...) })
	}
}

func TestGraphPreviewMemoryDiscoveryLegacyFormatJSONAlias(t *testing.T) {
	bd := buildBDUnderTest(t)
	work, home := t.TempDir(), t.TempDir()
	graphPolicyCLI(t, bd, work, home, nil, "", "init", "--prefix", "lfmt", "--skip-hooks", "--skip-agents", "--non-interactive", "--json")
	const body = "Legacy exact map body — 雪\n"
	graphPolicyCLI(t, bd, work, home, nil, "", "remember", body, "--key", "legacy-plan", "--json")
	expected := map[string]any{"legacy-plan": body, "schema_version": float64(1)}
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"json-flag", []string{"memories", "--json"}},
		{"format-json", []string{"memories", "--format", "json"}},
		{"format-uppercase-json", []string{"memories", "--format", "JSON"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := graphPolicyCLI(t, bd, work, home, nil, "", tc.args...)
			var got map[string]any
			if err := json.Unmarshal([]byte(out), &got); err != nil || !reflect.DeepEqual(got, expected) {
				t.Fatalf("legacy map/schema changed: %s (%v)", out, err)
			}
		})
	}
}
