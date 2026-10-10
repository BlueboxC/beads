//go:build cgo

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Exercise startup and the real prime child: changing cmd.Dir alone still
// inherits a BEADS_DIR rewritten by startup in the caller workspace (#7095).
func TestCodexHookBinarySelectsPayloadWorkspace(t *testing.T) {
	bd := buildBDUnderTest(t)
	root := t.TempDir()
	home := filepath.Join(root, "home")
	caller := filepath.Join(root, "caller")
	target := filepath.Join(caller, "target")
	empty := filepath.Join(root, "empty")
	for _, dir := range []string{home, target, empty} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "BEADS_") || strings.HasPrefix(key, "BD_") || strings.HasPrefix(key, "DOLT_") || strings.HasPrefix(key, "GIT_") || key == "HOME" || strings.HasPrefix(key, "XDG_") {
			continue
		}
		env = append(env, entry)
	}
	env = append(env, "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, "config"), "XDG_CACHE_HOME="+filepath.Join(home, "cache"), "BD_DISABLE_METRICS=1", "BD_NO_HOOKS=true", "BD_IMPORT_AUTO=false", "BD_BACKUP_ENABLED=false", "DOLT_DISABLE_EVENT_FLUSH=1", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	run := func(dir string, payload []byte, args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bd, append([]string{"--sandbox"}, args...)...)
		cmd.Dir, cmd.Env, cmd.Stdin = dir, env, bytes.NewReader(payload)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("bd %v: %v\n%s", args, err, stderr.String())
		}
		return stdout.Bytes()
	}
	for _, ws := range []struct{ dir, marker string }{{caller, "CALLER_ONLY_MARKER"}, {target, "TARGET_ONLY_MARKER"}} {
		git := exec.Command("git", "init", "-q", ws.dir)
		git.Env = env
		if output, err := git.CombinedOutput(); err != nil {
			t.Fatalf("git init: %v %s", err, output)
		}
		git = exec.Command("git", "-C", ws.dir, "config", "core.hooksPath", ".git/hooks")
		git.Env = env
		if output, err := git.CombinedOutput(); err != nil {
			t.Fatalf("git hooks: %v %s", err, output)
		}
		run(ws.dir, nil, "init", "--quiet", "--prefix", "probe", "--skip-hooks", "--skip-agents")
		run(ws.dir, nil, "remember", ws.marker+" content", "--key", ws.marker)
	}
	run(caller, nil, "config", "set", "no-push", "true")
	for _, event := range []string{codexHookSessionStart, codexHookPreCompact, codexHookPostCompact, codexHookUserPromptSubmit} {
		payload, err := json.Marshal(codexHookInput{SessionID: "payload-workspace", CWD: target, HookEventName: event})
		if err != nil {
			t.Fatal(err)
		}
		output := run(caller, payload, "codex-hook", event)
		if event == codexHookPreCompact || event == codexHookPostCompact {
			if len(bytes.TrimSpace(output)) != 0 {
				t.Fatalf("unexpected %s output: %s", event, output)
			}
			continue
		}
		var response codexHookResponse
		if err := json.Unmarshal(output, &response); err != nil {
			t.Fatal(err)
		}
		context := response.HookSpecificOutput.AdditionalContext
		if !strings.Contains(context, "TARGET_ONLY_MARKER") || strings.Contains(context, "CALLER_ONLY_MARKER") || strings.Contains(context, "Push disabled via config") {
			t.Fatalf("%s primed the wrong workspace:\n%s", event, context)
		}
	}
	payload, err := json.Marshal(codexHookInput{SessionID: "empty-workspace", CWD: empty, HookEventName: codexHookSessionStart})
	if err != nil {
		t.Fatal(err)
	}
	if output := run(caller, payload, "codex-hook", codexHookSessionStart); len(bytes.TrimSpace(output)) != 0 {
		t.Fatalf("uninitialized payload workspace emitted foreign context: %s", output)
	}
}

func TestRunBdPrimeRejectsInvalidPayloadDirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"relative", filepath.Join(t.TempDir(), "missing"), file} {
		if output, err := runBdPrimeInDir(context.Background(), dir); err == nil || output != "" {
			t.Fatalf("cwd %q: output=%q error=%v", dir, output, err)
		}
	}
}
