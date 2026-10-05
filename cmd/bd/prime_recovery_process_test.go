//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A failed memory-plane read produces an advisory exit-zero prime response.
// Only the actual hook/child protocol can prove it retains the refresh.
func TestPrimeHookUnavailableStoreRetainsRefreshUntilMemoryDelivery(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 for the real hook/driver boundary")
	}
	binary := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, binary, "--prefix=pr", "--stealth", "--skip-hooks", "--skip-agents", "--role=maintainer")
	cacheDir := filepath.Join(dir, "cache")
	if runtime.GOOS == "darwin" {
		cacheDir = filepath.Join(dir, "Library", "Caches")
	}
	run := func(input string, args ...string) (string, string, error) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), binary, append([]string{"--sandbox"}, args...)...)
		cmd.Dir = dir
		cmd.Env = append(bdEnv(dir), "XDG_CACHE_HOME="+cacheDir, "LOCALAPPDATA="+cacheDir, "BD_NO_HOOKS=true", "BD_BACKUP_ENABLED=false")
		cmd.Stdin = strings.NewReader(input)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		return stdout.String(), stderr.String(), err
	}
	mustRun := func(input string, args ...string) string {
		t.Helper()
		stdout, stderr, err := run(input, args...)
		if err != nil {
			t.Fatalf("bd %v: %v\n%s\n%s", args, err, stdout, stderr)
		}
		return stdout
	}
	// An empty healthy plane succeeds, and a memory quoting the advisory words
	// is ordinary data, not a failed projection.
	if out := mustRun("", "prime", "--require-memory-load", "--memories-only"); !strings.Contains(out, "No memories stored") {
		t.Fatal(out)
	}
	const solved = "Keep the solved module. Skipped: beads storage unavailable is quoted data."
	mustRun("", "remember", solved, "--key=solved")
	preserved := mustRun("", "--readonly", "recall", "solved", "--json")
	if err := os.WriteFile(filepath.Join(beadsDir, "PRIME.md"), []byte("CUSTOM WORKFLOW\n"), 0600); err != nil {
		t.Fatal(err)
	}
	input := codexHookInput{SessionID: "busy-store-session", CWD: dir}
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	mustRun(string(data), "codex-hook", codexHookPostCompact)
	marker := agentHookMarkerPath(filepath.Join(cacheDir, "beads", "codex-hooks"), input.SessionID, input.CWD)
	if _, err := os.Stat(marker); err != nil {
		t.Fatal(err)
	}
	metadataPath := filepath.Join(beadsDir, "metadata.json")
	originalMetadata, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(originalMetadata, &cfg); err != nil {
		t.Fatal(err)
	}
	// A temporary invalid database selector deterministically makes the normal
	// factory refuse the read; no lockfiles or storage internals are manipulated.
	cfg["dolt_database"] = "invalid/database"
	unavailable, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metadataPath, unavailable, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.WriteFile(metadataPath, originalMetadata, 0600) })
	if out := mustRun("", "prime"); !strings.Contains(out, "persistent memories were NOT injected") {
		t.Fatalf("unavailable store did not produce the advisory regression trigger: %s", out)
	}
	for _, event := range []string{codexHookSessionStart, codexHookUserPromptSubmit} {
		out := mustRun(string(data), "codex-hook", event)
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("%s consumed refresh without memories: %v; output=%s", event, err, out)
		}
		if strings.Contains(out, "additionalContext") {
			t.Fatalf("%s injected an incomplete memory projection: %s", event, out)
		}
	}
	if out := mustRun(string(data), "codex-hook", codexHookPreCompact); !strings.Contains(out, "context check failed") {
		t.Fatalf("PreCompact accepted the diagnostic: %s", out)
	}
	if err := os.WriteFile(metadataPath, originalMetadata, 0600); err != nil {
		t.Fatal(err)
	}
	out := mustRun(string(data), "codex-hook", codexHookUserPromptSubmit)
	if !strings.Contains(out, "CUSTOM WORKFLOW") || !strings.Contains(out, "Keep the solved module") {
		t.Fatalf("retry lost workflow/memories: %s", out)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("successful retry retained marker: %v", err)
	}
	if out := mustRun(string(data), "codex-hook", codexHookUserPromptSubmit); strings.TrimSpace(out) != "" {
		t.Fatalf("successful projection repeated: %s", out)
	}
	if out := mustRun("", "--readonly", "recall", "solved", "--json"); out != preserved {
		t.Fatal("context recovery changed the solved memory")
	}
}
