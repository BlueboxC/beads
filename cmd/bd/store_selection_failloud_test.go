//go:build cgo

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// writeCorruptMetadata creates a .beads dir whose metadata.json exists but
// cannot be parsed — the state a reader sees when the file is caught
// mid-rewrite (os.WriteFile truncate window) or hit by a transient read
// failure under load.
func writeCorruptMetadata(t *testing.T) string {
	t.Helper()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"dolt_mode":"serv`), 0o600); err != nil {
		t.Fatalf("write corrupt metadata.json: %v", err)
	}
	return beadsDir
}

// A present-but-unloadable metadata.json must be a hard error, never a
// silent fall-through to the embedded store. In managed server-mode
// deployments the embedded directory is an empty relic, so the silent
// fallback answers every query with an empty result set and exit 0 —
// callers read "no work" where the real store has rows (false-empty).
func TestNewDoltStoreFromConfigCorruptMetadataFailsLoud(t *testing.T) {
	beadsDir := writeCorruptMetadata(t)
	store, err := newDoltStoreFromConfig(context.Background(), beadsDir)
	if err == nil {
		if store != nil {
			_ = store.Close()
		}
		t.Fatal("newDoltStoreFromConfig: want error for corrupt metadata.json, got nil (silent embedded fallback)")
	}
}

func TestNewReadOnlyStoreFromConfigCorruptMetadataFailsLoud(t *testing.T) {
	beadsDir := writeCorruptMetadata(t)
	store, err := newReadOnlyStoreFromConfig(context.Background(), beadsDir)
	if err == nil {
		if store != nil {
			_ = store.Close()
		}
		t.Fatal("newReadOnlyStoreFromConfig: want error for corrupt metadata.json, got nil (silent embedded fallback)")
	}
}

// loadServerModeFromBeadsDir feeds the serverMode globals that the primary
// store-init path consults; a swallowed load failure leaves serverMode=false
// and routes data commands to the embedded store. The error must surface.
func TestLoadServerModeFromBeadsDirCorruptMetadataReturnsError(t *testing.T) {
	beadsDir := writeCorruptMetadata(t)
	if err := loadServerModeFromBeadsDir(beadsDir); err == nil {
		t.Fatal("loadServerModeFromBeadsDir: want error for corrupt metadata.json, got nil")
	}
}

// A real initialized workspace must retain its rows across metadata refusal
// and explicit restoration. Doctor and ordinary init must not infer a backend
// or rewrite the selector; version remains available without opening a store.
func TestCorruptMetadataRefusalAndExplicitRestorePreserveData(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "cm")

	run := func(args ...string) (string, error) {
		cmd := exec.Command(bd, args...)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	if out, err := run("create", "--title", "Preserve rows during metadata recovery", "--type", "task"); err != nil {
		t.Fatalf("create preserved issue: %v\n%s", err, out)
	}
	before, err := run("list", "--json")
	if err != nil || !strings.Contains(before, "Preserve rows during metadata recovery") {
		t.Fatalf("read preserved issue: %v\n%s", err, before)
	}
	metadataPath := filepath.Join(beadsDir, "metadata.json")
	validMetadata, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("read valid metadata: %v", err)
	}
	corruptMetadata := []byte(`{"dolt_mode":"serv`)
	if err := os.WriteFile(metadataPath, corruptMetadata, 0o600); err != nil {
		t.Fatalf("corrupt metadata.json: %v", err)
	}
	if out, err := run("version"); err != nil {
		t.Fatalf("bd version with corrupt metadata: %v\n%s", err, out)
	}
	for _, args := range [][]string{{"doctor"}, {"doctor", "--json"}, {"list", "--json"}, {"init", "--prefix", "cm"}} {
		out, err := run(args...)
		if err == nil || !strings.Contains(out, "metadata.json") {
			t.Fatalf("bd %s: want named metadata refusal, got %v\n%s", strings.Join(args, " "), err, out)
		}
		if args[0] == "doctor" && !strings.Contains(out, "no storage database was opened or modified") {
			t.Errorf("doctor refusal lacks storage-safety diagnosis:\n%s", out)
		}
		after, err := os.ReadFile(metadataPath)
		if err != nil || string(after) != string(corruptMetadata) {
			t.Fatalf("bd %s changed corrupt metadata: %v\n%s", strings.Join(args, " "), err, after)
		}
	}

	// Restore the known-valid selector, not a newly inferred database config.
	if err := os.WriteFile(metadataPath, validMetadata, 0o600); err != nil {
		t.Fatalf("restore valid metadata: %v", err)
	}
	if after, err := run("list", "--json"); err != nil || after != before {
		t.Fatalf("rows changed after explicit metadata restoration: %v\nbefore: %s\nafter: %s", err, before, after)
	}
}

// Absent metadata.json stays a legitimate fresh-repo default: no error.
func TestLoadServerModeFromBeadsDirAbsentMetadataIsFine(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := loadServerModeFromBeadsDir(beadsDir); err != nil {
		t.Fatalf("loadServerModeFromBeadsDir: want nil for absent metadata.json, got %v", err)
	}
}
