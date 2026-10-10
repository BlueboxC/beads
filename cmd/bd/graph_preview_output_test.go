package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

type graphFailingWriter struct {
	err   error
	calls int
}

func (w *graphFailingWriter) Write([]byte) (int, error) { w.calls++; return 0, w.err }

func TestGraphPreviewOutputFailures(t *testing.T) {
	failure := errors.New("output device failed")
	for _, structured := range []bool{false, true} {
		w := &graphFailingWriter{err: failure}
		if err := graphPrintTo(w, map[string]any{"changes": []any{}}, "complete comparison", false, structured); !errors.Is(err, failure) || w.calls == 0 {
			t.Fatalf("structured=%v output failure lost: %v", structured, err)
		}
	}
	w := &graphFailingWriter{err: failure}
	if err := graphPrintTo(w, nil, "quiet", true, false); err != nil || w.calls != 0 {
		t.Fatalf("quiet human wrote: %v", err)
	}
	if err := graphPrintTo(w, nil, "quiet", true, true); !errors.Is(err, failure) {
		t.Fatalf("quiet JSON hid output failure: %v", err)
	}
	var out bytes.Buffer
	if err := graphPrintTo(&out, nil, "whole result", false, false); err != nil || out.String() != "whole result\n" {
		t.Fatalf("human framing changed: %q %v", out.String(), err)
	}
}

func TestGraphPreviewCompleteRecordJSONUsesRevisionOnly(t *testing.T) {
	record := map[string]any{
		"id": "https://example.invalid/scope/beads/plan", "type": "https://example.invalid/scope/types/memory",
		"revision": "opaque", "version": "opaque",
		"properties": map[string]any{"version": "user value", "large": json.Number("9007199254740993")},
		"owned": []any{map[string]any{
			"id": "https://example.invalid/scope/links/context", "type": "https://example.invalid/scope/types/related",
			"revision": "link-token", "version": "link-token", "properties": map[string]any{"version": "link property"},
		}},
	}
	result := map[string]any{
		"memory":   record,
		"versions": []any{map[string]any{"version": "retained-token", "local_revision": 2}},
		"removed":  map[string]any{"id": "links/old", "type": "related", "revision": "deletion-marker", "version": "deletion-marker", "state": "deleted"},
		"changes": []any{
			map[string]any{"area": "owned", "to": map[string]any{"present": true, "value": map[string]any{"id": "https://example.invalid/scope/links/context", "type": "related", "revision": "owned", "version": "owned", "properties": map[string]any{}}}},
			map[string]any{"area": "properties", "to": map[string]any{"present": true, "value": map[string]any{"id": "user", "type": "user", "revision": "user", "version": "user", "properties": map[string]any{}}}},
		},
	}
	var out bytes.Buffer
	if err := graphPrintTo(&out, result, "", false, true); err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	var memory map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Result["memory"], &memory); err != nil {
		t.Fatal(err)
	}
	if _, exists := memory["version"]; exists {
		t.Fatalf("complete Memory retained duplicate version: %s", out.String())
	}
	if string(memory["revision"]) != `"opaque"` || !bytes.Contains(memory["properties"], []byte(`9007199254740993`)) || !bytes.Contains(memory["properties"], []byte(`"version":"user value"`)) {
		t.Fatalf("revision or user properties changed: %s", out.String())
	}
	if bytes.Contains(memory["owned"], []byte(`"version":"link-token"`)) || !bytes.Contains(memory["owned"], []byte(`"version":"link property"`)) {
		t.Fatalf("owned Link projection changed the wrong member: %s", out.String())
	}
	if !bytes.Contains(envelope.Result["versions"], []byte(`"version":"retained-token"`)) || !bytes.Contains(envelope.Result["removed"], []byte(`"version":"deletion-marker"`)) {
		t.Fatalf("retained address or deletion marker was renamed: %s", out.String())
	}
	if bytes.Contains(envelope.Result["changes"], []byte(`"version":"owned"`)) || !bytes.Contains(envelope.Result["changes"], []byte(`"version":"user"`)) {
		t.Fatalf("owned comparison or arbitrary property value changed: %s", out.String())
	}
}
