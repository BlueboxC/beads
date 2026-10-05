package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCompactDiagnosticHistoryIsBoundedAndSurvivesLatestOverwrite(t *testing.T) {
	codexHookMarkerDirOverride = t.TempDir()
	t.Cleanup(func() { codexHookMarkerDirOverride = "" })
	input := codexHookInput{SessionID: "PRIVATE SESSION", CWD: "PRIVATE CWD", Source: "compact", TurnID: "01a10cbb-8077-7a73-bdfb-a7c8785b1967"}
	for i := 0; i < 20; i++ {
		d := beginCodexHookDiagnostic(codexHookSessionStart, input)
		d.StartedAt = time.Date(2026, 10, 5, 0, 0, i, 0, time.UTC)
		d.PrimeResult = "loaded"
		d.OutputResult = "context_written"
		d.finish(nil)
	}
	dir := strings.TrimSuffix(codexHookRefreshMarkerPath(input), ".refresh") + ".attempts"
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 16 {
		t.Fatalf("archive bound: %d %v", len(entries), err)
	}
	before := []string{}
	for _, e := range entries {
		before = append(before, e.Name())
	}
	d := beginCodexHookDiagnostic(codexHookSessionStart, codexHookInput{SessionID: input.SessionID, CWD: input.CWD, Source: "resume"})
	d.finish(nil)
	for _, name := range before {
		data, err := os.ReadFile(dir + "/" + name)
		if err != nil {
			t.Fatal("latest overwrite erased compact attempt")
		}
		info, err := os.Stat(dir + "/" + name)
		if err != nil || info.Mode().Perm() != 0600 || len(data) > 2048 || bytes.Contains(data, []byte("PRIVATE")) {
			t.Fatal("history must retain private bounded metadata only")
		}
		var row codexHookDiagnostic
		if err := json.Unmarshal(data, &row); err != nil || row.Source != "compact" {
			t.Fatal("compact identity lost")
		}
	}
}
