//go:build cgo && !windows

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// This covers command dispatch, the glab subprocess and persisted dry-run/closure
// boundaries that the injected provider tests cannot establish.
func TestGitLabGateProcessWorkflow(t *testing.T) {
	bd := buildBDUnderTest(t)
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "core.hooksPath", ".git/hooks"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-qm", "fixture"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git := exec.Command("git", "rev-parse", "HEAD")
	git.Dir = dir
	head, err := git.Output()
	if err != nil {
		t.Fatal(err)
	}
	git = exec.Command("git", "symbolic-ref", "--short", "HEAD")
	git.Dir = dir
	branch, err := git.Output()
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	response := filepath.Join(binDir, "response.json")
	if err := os.WriteFile(filepath.Join(binDir, "glab"), []byte("#!/bin/sh\n[ \"$1 $2 $3 $4 $5\" = 'api --method GET --output json' ] || exit 7\ncat '"+response+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BD_DISABLE_METRICS", "1")
	t.Setenv("DOLT_DISABLE_EVENT_FLUSH", "1")
	runBDStdout(t, bd, dir, "--sandbox", "init", "--prefix", "fixture", "--skip-hooks", "--skip-agents")
	target := runBDStdout(t, bd, dir, "--sandbox", "q", "Provider workflow")
	created := runBDStdout(t, bd, dir, "--sandbox", "gate", "create", "--type=gl:pipeline", "--blocks", target)
	fields := strings.Fields(created)
	if len(fields) < 4 {
		t.Fatalf("gate create: %s", created)
	}
	id := fields[3]
	show := func() *types.Issue {
		t.Helper()
		var issue types.Issue
		if err := json.Unmarshal([]byte(runBDStdout(t, bd, dir, "--sandbox", "gate", "show", id, "--json")), &issue); err != nil {
			t.Fatal(err)
		}
		return &issue
	}
	// Workspace setup may have committed its own configuration; use its actual HEAD.
	git = exec.Command("git", "rev-parse", "HEAD")
	git.Dir = dir
	head, err = git.Output()
	if err != nil {
		t.Fatal(err)
	}
	list, err := json.Marshal([]map[string]interface{}{{"id": 42, "ref": strings.TrimSpace(string(branch)), "sha": strings.TrimSpace(string(head))}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(response, list, 0600); err != nil {
		t.Fatal(err)
	}
	runBDStdout(t, bd, dir, "--sandbox", "gate", "discover", "--type=gl:pipeline", "--dry-run")
	if show().AwaitID != "" {
		t.Fatal("dry-run persisted an ID")
	}
	runBDStdout(t, bd, dir, "--sandbox", "gate", "discover", "--type=gl:pipeline")
	if show().AwaitID != "42" {
		t.Fatal("discovered ID not persisted")
	}
	if err := os.WriteFile(response, []byte(`{"id":42,"status":"success"}`), 0600); err != nil {
		t.Fatal(err)
	}
	runBDStdout(t, bd, dir, "--sandbox", "gate", "check", "--type=gl", "--dry-run")
	if show().Status != types.StatusOpen {
		t.Fatal("dry-run closed gate")
	}
	runBDStdout(t, bd, dir, "--sandbox", "gate", "check", "--type=gl")
	if show().Status != types.StatusClosed {
		t.Fatal("successful pipeline not closed")
	}
}
