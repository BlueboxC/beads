//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/utils"
)

// Exercise early startup selection before the ordinary dotenv loader runs.
func TestProjectSelectionEmptyEnvironmentKeepsWorkspace(t *testing.T) {
	bd := buildBDUnderTest(t)
	for _, selector := range []string{"BEADS_DIR", "BEADS_DB", "BD_DB"} {
		t.Run(selector, func(t *testing.T) {
			local, foreign := t.TempDir(), t.TempDir()
			for _, dir := range []string{local, foreign} {
				writeTestConfigYAML(t, filepath.Join(dir, ".beads"), "issue-prefix: fixture\n")
				// Explicit metadata avoids classifying this store-free fixture as legacy.
				if err := os.WriteFile(filepath.Join(dir, ".beads", "metadata.json"), []byte(`{"backend":"dolt","dolt_mode":"server","dolt_database":"fixture"}`), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Join(dir, ".beads", "dolt"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			target := filepath.Join(foreign, ".beads")
			if selector != "BEADS_DIR" {
				target = filepath.Join(target, "dolt")
			}
			if err := os.WriteFile(filepath.Join(local, ".beads", ".env"), []byte(selector+"="+target+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(bd, "--sandbox", "--readonly", "where", "--json")
			cmd.Dir = local
			cmd.Env = envWithout(bdEnv(local), "BD_DB")
			cmd.Env = append(cmd.Env, "BEADS_DIR=", "BEADS_DB=", "BD_DB=")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("where: %v\n%s", err, out)
			}
			var result WhereResult
			if err := json.Unmarshal(out, &result); err != nil {
				t.Fatal(err)
			}
			if got, want := utils.CanonicalizePath(result.Path), utils.CanonicalizePath(filepath.Join(local, ".beads")); got != want {
				t.Fatalf("workspace = %s, want %s", got, want)
			}
		})
	}
}
