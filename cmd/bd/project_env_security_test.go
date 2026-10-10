package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestProjectEnvExecutionBoundary(t *testing.T) {
	for _, key := range []string{"BEADS_DOLT_CREDENTIAL_COMMAND", "BEADS_DOLT_PASSWORD_COMMAND", "BEADS_DOLT_BIN", "PATH", "PAGER", "BD_PAGER", "BASH_ENV", "LD_PRELOAD", "DYLD_INSERT_LIBRARIES", "ComSpec", "PATHEXT"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, "")
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(key+"=project_executable\nBEADS_DOLT_PASSWORD=static_password\n"), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("BEADS_DOLT_PASSWORD", "")
			if err := os.Unsetenv("BEADS_DOLT_PASSWORD"); err != nil {
				t.Fatal(err)
			}
			loadBeadsEnvFile(dir)
			if _, ok := os.LookupEnv(key); ok {
				t.Errorf("project imported executable setting %s", key)
			}
			if got := os.Getenv("BEADS_DOLT_PASSWORD"); got != "static_password" {
				t.Errorf("static credential = %q", got)
			}
		})
	}
	t.Run("operator helper and empty setting win", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_CREDENTIAL_COMMAND", "operator_helper")
		t.Setenv("BEADS_DOLT_PASSWORD", "")
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("BEADS_DOLT_CREDENTIAL_COMMAND=project_helper\nBEADS_DOLT_PASSWORD=project_password\n"), 0600); err != nil {
			t.Fatal(err)
		}
		loadBeadsEnvFile(dir)
		if got := os.Getenv("BEADS_DOLT_CREDENTIAL_COMMAND"); got != "operator_helper" {
			t.Errorf("operator helper = %q", got)
		}
		if got := os.Getenv("BEADS_DOLT_PASSWORD"); got != "" {
			t.Errorf("explicit empty value overwritten: %q", got)
		}
	})
}

func TestProjectEnvKeyIdentity(t *testing.T) {
	t.Setenv("BEADS_DOLT_PASSWORD", "")
	if err := os.Unsetenv("BEADS_DOLT_PASSWORD"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("beads_dolt_password=static_password\n"), 0600); err != nil {
		t.Fatal(err)
	}
	loadBeadsEnvFile(dir)
	want := ""
	if runtime.GOOS == "windows" {
		want = "static_password"
	}
	if got := os.Getenv("BEADS_DOLT_PASSWORD"); got != want {
		t.Errorf("password = %q, want %q", got, want)
	}
}

func TestProjectSelectionEnvPreservesPresence(t *testing.T) {
	for _, key := range []string{"BEADS_DIR", "BEADS_DB", "BD_DB"} {
		for _, state := range []string{"absent", "empty", "operator"} {
			t.Run(key+"/"+state, func(t *testing.T) {
				dir := t.TempDir()
				writeTestConfigYAML(t, filepath.Join(dir, ".beads"), "")
				t.Chdir(dir)
				for _, selector := range []string{"BEADS_DIR", "BEADS_DB", "BD_DB"} {
					t.Setenv(selector, "")
					if err := os.Unsetenv(selector); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(filepath.Join(dir, ".beads", ".env"), []byte(key+"=project_selection\n"), 0600); err != nil {
					t.Fatal(err)
				}
				want := "project_selection"
				if state == "empty" {
					t.Setenv(key, "")
					want = ""
				} else if state == "operator" {
					t.Setenv(key, "operator_selection")
					want = "operator_selection"
				}
				loadSelectionEnvironment()
				if got, present := os.LookupEnv(key); !present || got != want {
					t.Fatalf("%s = %q (present=%v), want %q", key, got, present, want)
				}
			})
		}
	}
}
