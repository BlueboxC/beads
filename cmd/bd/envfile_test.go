package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadBeadsEnvFile(t *testing.T) {
	t.Run("loads env vars from .env file", func(t *testing.T) {
		dir := t.TempDir()
		envFile := filepath.Join(dir, ".env")
		if err := os.WriteFile(envFile, []byte("BEADS_DOLT_PASSWORD=hello_from_env\n"), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BEADS_DOLT_PASSWORD", "") // clear
		os.Unsetenv("BEADS_DOLT_PASSWORD")

		loadBeadsEnvFile(dir)

		if got := os.Getenv("BEADS_DOLT_PASSWORD"); got != "hello_from_env" {
			t.Errorf("expected BEADS_DOLT_PASSWORD=hello_from_env, got %q", got)
		}
		os.Unsetenv("BEADS_DOLT_PASSWORD")
	})

	t.Run("shell env takes precedence over .env", func(t *testing.T) {
		dir := t.TempDir()
		envFile := filepath.Join(dir, ".env")
		if err := os.WriteFile(envFile, []byte("BEADS_DOLT_PASSWORD=from_file\n"), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BEADS_DOLT_PASSWORD", "from_shell")

		loadBeadsEnvFile(dir)

		if got := os.Getenv("BEADS_DOLT_PASSWORD"); got != "from_shell" {
			t.Errorf("expected shell env to win, got %q", got)
		}
	})

	t.Run("no-op when .env does not exist", func(t *testing.T) {
		dir := t.TempDir()
		// Should not panic or error
		loadBeadsEnvFile(dir)
	})

	t.Run("no-op when beadsDir is empty", func(t *testing.T) {
		// Should not panic or error
		loadBeadsEnvFile("")
	})
}
