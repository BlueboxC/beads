package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Shared helpers for the agent lifecycle hook commands (codex-hook, cursor-hook).
// Each agent keeps its own event names, input/output schemas, and test stubs, but
// the prime-runner and one-shot refresh-marker mechanics are identical and live
// here to avoid divergence.

var agentHookStartupEnv = os.Environ()

// runBdPrime shells out to `bd prime [args...]` and returns its combined output.
// The hooks exec a subprocess (rather than calling prime in process) to avoid
// re-entrant store initialization. Required memory loading distinguishes a
// healthy empty workspace from an advisory diagnostic without recalling data.
func runBdPrime(ctx context.Context, args ...string) (string, error) {
	return runBdPrimeInDir(ctx, "", args...)
}

// runBdPrimeInDir uses the event workspace and the original operator environment,
// excluding path/database selectors rewritten by startup in the caller workspace.
func runBdPrimeInDir(ctx context.Context, cwd string, args ...string) (string, error) {
	if cwd != "" {
		if !filepath.IsAbs(cwd) {
			return "", fmt.Errorf("hook cwd must be absolute")
		}
		info, err := os.Stat(cwd)
		if err != nil {
			return "", fmt.Errorf("hook cwd: %w", err)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("hook cwd is not a directory")
		}
	}
	cmdArgs := append([]string{"prime", "--" + primeRequireMemoryLoadFlag}, args...)
	// #nosec G702 - os.Args[0] is this bd binary re-invoking itself; cmdArgs is the
	// fixed "prime" subcommand plus internal flags, never attacker-controlled input.
	cmd := exec.CommandContext(ctx, os.Args[0], cmdArgs...)
	if cwd != "" {
		cmd.Dir = cwd
		cmd.Env = make([]string, 0, len(agentHookStartupEnv)+4)
		for _, entry := range agentHookStartupEnv {
			key, _, _ := strings.Cut(entry, "=")
			switch strings.ToUpper(key) {
			case "BEADS_DIR", "BEADS_DB", "BD_DB", "BEADS_DOLT_SERVER_DATABASE":
				continue
			}
			cmd.Env = append(cmd.Env, entry)
		}
		cmd.Env = append(cmd.Env, "BEADS_DIR=", "BEADS_DB=", "BD_DB=", "BEADS_DOLT_SERVER_DATABASE=")
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("bd %s: %w: %s", strings.Join(cmdArgs, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// agentHookMarkerBaseDir returns the cache directory for one-shot
// post-compaction refresh markers for a given agent (subdir e.g. "codex-hooks").
// override redirects the location for tests.
func agentHookMarkerBaseDir(subdir, override string) string {
	if override != "" {
		return override
	}
	if dir, err := os.UserCacheDir(); err == nil && dir != "" {
		return filepath.Join(dir, "beads", subdir)
	}
	return filepath.Join(os.TempDir(), "beads-"+subdir)
}

// agentHookMarkerPath derives a per-session, per-workspace marker file under
// base so concurrent agent sessions don't clobber each other's state. Empty
// keys fall back to stable placeholders.
func agentHookMarkerPath(base, sessionKey, workspaceKey string) string {
	if sessionKey == "" {
		sessionKey = "unknown-session"
	}
	if workspaceKey == "" {
		workspaceKey = "unknown-workspace"
	}
	sum := sha256.Sum256([]byte(sessionKey + "\x00" + filepath.Clean(workspaceKey)))
	return filepath.Join(base, hex.EncodeToString(sum[:])+".refresh")
}

// writeAgentHookMarker creates the marker directory and writes the one-shot
// refresh marker file.
func writeAgentHookMarker(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte("1\n"), 0o600) // #nosec G306 -- user-private cache marker
}
