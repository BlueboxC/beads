package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/git"
)

func TestBootstrap_WorktreeFallbackDir(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-bootstrap-worktree-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	mainRepoDir := filepath.Join(tmpDir, "main-repo")
	if err := os.MkdirAll(mainRepoDir, 0755); err != nil {
		t.Fatal(err)
	}

	run := func(dir string, args ...string) {
		runGitForBootstrapTest(t, dir, args...)
	}

	run(mainRepoDir, "init")
	run(mainRepoDir, "config", "user.email", "test@example.com")
	run(mainRepoDir, "config", "user.name", "Test User")
	run(mainRepoDir, "config", "core.hooksPath", ".git/hooks")

	if err := os.WriteFile(filepath.Join(mainRepoDir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run(mainRepoDir, "add", "README.md")
	run(mainRepoDir, "commit", "-m", "Initial commit")

	worktreeDir := filepath.Join(tmpDir, "worktree")
	cmd := exec.Command("git", "worktree", "add", worktreeDir, "HEAD")
	cmd.Dir = mainRepoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add failed: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		cleanupCmd := exec.Command("git", "worktree", "remove", "--force", worktreeDir)
		cleanupCmd.Dir = mainRepoDir
		_ = cleanupCmd.Run()
	})

	mainBeadsDir := filepath.Join(mainRepoDir, ".beads")
	if err := os.MkdirAll(filepath.Join(mainBeadsDir, "dolt"), 0755); err != nil {
		t.Fatal(err)
	}

	worktreeBeadsDir := filepath.Join(worktreeDir, ".beads")
	_ = os.RemoveAll(worktreeBeadsDir)

	t.Chdir(worktreeDir)
	git.ResetCaches()

	if !git.IsWorktree() {
		t.Fatal("expected git.IsWorktree() to return true in worktree")
	}

	got := beads.GetWorktreeFallbackBeadsDir()
	if got == "" {
		t.Fatal("GetWorktreeFallbackBeadsDir() returned empty string")
	}

	gotClean := filepath.Clean(strings.TrimSpace(got))
	wantClean := filepath.Clean(strings.TrimSpace(mainBeadsDir))

	gotResolved, err := filepath.EvalSymlinks(gotClean)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q) failed: %v", gotClean, err)
	}
	wantResolved, err := filepath.EvalSymlinks(wantClean)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q) failed: %v", wantClean, err)
	}

	if gotResolved != wantResolved {
		t.Errorf("GetWorktreeFallbackBeadsDir() = %q (resolved %q), want %q (resolved %q)",
			gotClean, gotResolved, wantClean, wantResolved)
	}

	cwd, _ := os.Getwd()
	cwdBeads := filepath.Join(cwd, ".beads")
	cwdResolved, _ := filepath.EvalSymlinks(filepath.Clean(cwdBeads))
	if gotResolved == cwdResolved {
		t.Errorf("GetWorktreeFallbackBeadsDir() returned CWD-based path %q, expected main repo path", gotResolved)
	}
}

func TestBootstrap_WorktreeLocalBeadsPreferLocal(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-bootstrap-local-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	mainRepoDir := filepath.Join(tmpDir, "main-repo")
	if err := os.MkdirAll(mainRepoDir, 0755); err != nil {
		t.Fatal(err)
	}

	run := func(dir string, args ...string) {
		runGitForBootstrapTest(t, dir, args...)
	}
	run(mainRepoDir, "init")
	run(mainRepoDir, "config", "user.email", "test@example.com")
	run(mainRepoDir, "config", "user.name", "Test User")
	run(mainRepoDir, "config", "core.hooksPath", ".git/hooks")
	if err := os.WriteFile(filepath.Join(mainRepoDir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run(mainRepoDir, "add", "README.md")
	run(mainRepoDir, "commit", "-m", "Initial commit")

	worktreeDir := filepath.Join(tmpDir, "worktree")
	cmd := exec.Command("git", "worktree", "add", worktreeDir, "HEAD")
	cmd.Dir = mainRepoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add failed: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		cleanupCmd := exec.Command("git", "worktree", "remove", "--force", worktreeDir)
		cleanupCmd.Dir = mainRepoDir
		_ = cleanupCmd.Run()
	})

	mainBeadsDir := filepath.Join(mainRepoDir, ".beads")
	worktreeBeadsDir := filepath.Join(worktreeDir, ".beads")
	if err := os.MkdirAll(filepath.Join(mainBeadsDir, "dolt"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(worktreeBeadsDir, "dolt"), 0755); err != nil {
		t.Fatal(err)
	}

	t.Chdir(worktreeDir)
	git.ResetCaches()

	if !git.IsWorktree() {
		t.Fatal("expected git.IsWorktree() to return true")
	}

	found := beads.FindBeadsDir()
	if found == "" {
		t.Fatal("FindBeadsDir() returned empty")
	}

	foundResolved, _ := filepath.EvalSymlinks(filepath.Clean(found))
	worktreeBeadsResolved, _ := filepath.EvalSymlinks(filepath.Clean(worktreeBeadsDir))

	if foundResolved != worktreeBeadsResolved {
		t.Errorf("FindBeadsDir() = %q, want %q (local worktree .beads should take precedence over main repo fallback)",
			foundResolved, worktreeBeadsResolved)
	}
}

func TestBootstrap_WorktreeNoBeadsAnywhereStillPointsToMainRepo(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-bootstrap-empty-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	mainRepoDir := filepath.Join(tmpDir, "main-repo")
	if err := os.MkdirAll(mainRepoDir, 0755); err != nil {
		t.Fatal(err)
	}

	run := func(dir string, args ...string) {
		runGitForBootstrapTest(t, dir, args...)
	}
	run(mainRepoDir, "init")
	run(mainRepoDir, "config", "user.email", "test@example.com")
	run(mainRepoDir, "config", "user.name", "Test User")
	run(mainRepoDir, "config", "core.hooksPath", ".git/hooks")
	if err := os.WriteFile(filepath.Join(mainRepoDir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run(mainRepoDir, "add", "README.md")
	run(mainRepoDir, "commit", "-m", "Initial commit")

	worktreeDir := filepath.Join(tmpDir, "worktree")
	cmd := exec.Command("git", "worktree", "add", worktreeDir, "HEAD")
	cmd.Dir = mainRepoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add failed: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		cleanupCmd := exec.Command("git", "worktree", "remove", "--force", worktreeDir)
		cleanupCmd.Dir = mainRepoDir
		_ = cleanupCmd.Run()
	})

	os.RemoveAll(filepath.Join(worktreeDir, ".beads"))

	t.Chdir(worktreeDir)
	git.ResetCaches()

	fallback := beads.GetWorktreeFallbackBeadsDir()
	if fallback == "" {
		t.Fatal("GetWorktreeFallbackBeadsDir() returned empty")
	}

	mainBeadsDir := filepath.Join(mainRepoDir, ".beads")
	fallbackResolved, _ := filepath.EvalSymlinks(filepath.Clean(fallback))
	mainResolved, _ := filepath.EvalSymlinks(filepath.Clean(mainBeadsDir))
	if fallbackResolved != mainResolved {
		t.Errorf("GetWorktreeFallbackBeadsDir() = %q, want %q (should resolve to main repo even without .beads)", fallbackResolved, mainResolved)
	}
}

// A real Git trace detects detached maintenance even when the worktree race
// does not happen on this run. Repository settings deliberately enable it.
func TestBootstrapGitFixture_DoesNotStartAutomaticMaintenance(t *testing.T) {
	repo := t.TempDir()
	initGitRepoAt(t, repo)
	runGitForBootstrapTest(t, repo, "config", "maintenance.auto", "true")
	runGitForBootstrapTest(t, repo, "config", "gc.auto", "1")
	trace := filepath.Join(t.TempDir(), "git-trace.json")
	t.Setenv("GIT_TRACE2_EVENT", trace)
	runGitForBootstrapTest(t, repo, "commit", "--allow-empty", "-m", "initial")
	worktree := filepath.Join(t.TempDir(), "linked")
	runGitForBootstrapTest(t, repo, "worktree", "add", "--detach", worktree, "HEAD")
	if _, err := os.Stat(filepath.Join(repo, ".git", "worktrees", "linked", "gitdir")); err != nil {
		t.Fatalf("worktree administration missing: %v", err)
	}
	body, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		var event struct {
			Event string   `json:"event"`
			Argv  []string `json:"argv"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event.Event == "child_start" {
			for _, arg := range event.Argv {
				if arg == "maintenance" || arg == "gc" {
					t.Fatalf("fixture launched detached maintenance: %v", event.Argv)
				}
			}
		}
	}
}
