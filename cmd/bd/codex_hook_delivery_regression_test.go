package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestColdSessionStartOutputFailureRetainsNextPromptRefresh(t *testing.T) {
	codexHookMarkerDirOverride = t.TempDir()
	t.Cleanup(func() { codexHookMarkerDirOverride = "" })
	stubCodexHookPrime(t, func(bool) (string, error) { return "Preserved solution\n", nil })
	input := codexHookInput{SessionID: "cold", CWD: "/repo"}
	payload := `{"session_id":"cold","cwd":"/repo"}`
	if err := runCodexHook(context.Background(), codexHookSessionStart, strings.NewReader(payload), codexHookErrorWriter{}); err == nil {
		t.Fatal("failed output accepted")
	}
	if _, err := os.Stat(codexHookRefreshMarkerPath(input)); err != nil {
		t.Fatal("failed cold delivery discarded fallback")
	}
	var out bytes.Buffer
	if err := runCodexHook(context.Background(), codexHookUserPromptSubmit, strings.NewReader(payload), &out); err != nil {
		t.Fatal(err)
	}
	var response codexHookResponse
	if err := json.Unmarshal(out.Bytes(), &response); err != nil || response.HookSpecificOutput.AdditionalContext != "Preserved solution\n" {
		t.Fatal("next prompt did not recover complete context")
	}
	if _, err := os.Stat(codexHookRefreshMarkerPath(input)); !os.IsNotExist(err) {
		t.Fatal("successful recovery did not consume fallback")
	}
}
