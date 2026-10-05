package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCodexActivityHooksKeepOtherHandlersAndPluginParity(t *testing.T) {
	var current map[string]interface{}
	seed := `{"hooks":{"PostToolUse":[{"hooks":[{"type":"command","command":"echo keep"},{"type":"command","command":"bd codex-activity PostToolUse"}]}]}}`
	if err := json.Unmarshal([]byte(seed), &current); err != nil {
		t.Fatal(err)
	}
	upsertCodexManagedHooks(current)
	first, _ := json.Marshal(current)
	if !codexManagedHooksCurrent(current) {
		t.Fatal("activity definitions missing")
	}
	upsertCodexManagedHooks(current)
	second, _ := json.Marshal(current)
	if string(first) != string(second) {
		t.Fatal("setup duplicated activity handlers")
	}
	removeCodexManagedHooks(current)
	want := map[string]interface{}{}
	if err := json.Unmarshal([]byte(`{"hooks":{"PostToolUse":[{"hooks":[{"type":"command","command":"echo keep"}]}]}}`), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(current, want) {
		t.Fatalf("unrelated handler removed: %+v", current)
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "plugins", "beads", ".codex-plugin", "hooks", "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var plugin map[string]interface{}
	if err = json.Unmarshal(raw, &plugin); err != nil {
		t.Fatal(err)
	}
	generated, _ := json.Marshal(codexManagedHooks())
	packaged, _ := json.Marshal(plugin["hooks"])
	if string(generated) != string(packaged) {
		t.Fatal("plugin hooks differ from fallback")
	}
}
