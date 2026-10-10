//go:build cgo

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/graphstore"
	"github.com/steveyegge/beads/internal/templates/agents"
)

func TestGraphPreviewClaudeStopAutoInit(t *testing.T) {
	bd := buildBDUnderTest(t)
	for _, engine := range []string{"embedded", "server"} {
		t.Run(engine, func(t *testing.T) {
			work, home := t.TempDir(), t.TempDir()
			args := []string{"init", "--graph-mode", "link", "--scope-url", "https://example.invalid/auto-stop/", "--non-interactive", "--json"}
			if engine == "server" {
				port := os.Getenv("BEADS_GRAPH_TEST_SERVER_PORT")
				if port == "" {
					t.Skip("set BEADS_GRAPH_TEST_SERVER_PORT for ordinary shared-server CLI qualification")
				}
				args = append(args, "--server", "--external", "--server-host", "127.0.0.1", "--server-port", port, "--server-user", "root")
			}
			graphPolicyCLI(t, bd, work, home, nil, "", args...)
			graphPolicyCLI(t, bd, work, home, nil, "", "setup", "claude", "--check")
			settings, err := os.ReadFile(filepath.Join(work, ".claude", "settings.json"))
			if err != nil || bytes.Count(settings, []byte("bd claude-hook stop")) != 1 {
				t.Fatalf("init did not register one Stop hook: %v %s", err, settings)
			}
			claude, err := os.ReadFile(filepath.Join(work, "CLAUDE.md"))
			if err != nil || string(claude) != "@AGENTS.md\n" {
				t.Fatalf("init did not import graph guidance: %v %q", err, claude)
			}
			guidance, err := os.ReadFile(filepath.Join(work, "AGENTS.md"))
			if err != nil || !bytes.Contains(guidance, []byte("registers the project-local Claude Stop reminder by default")) {
				t.Fatalf("init guidance did not describe automatic Stop registration: %v %s", err, guidance)
			}
		})
	}
}

func TestGraphPreviewClaudeStopAutoInitPreflight(t *testing.T) {
	bd := buildBDUnderTest(t)
	work, home := t.TempDir(), t.TempDir()
	settings := filepath.Join(work, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"hooks":{"Stop":"not an array"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	graphPolicyCLI(t, bd, work, home, nil, "graph_not_initialized", "init", "--graph-mode", "link", "--scope-url", "https://example.invalid/auto-stop/", "--non-interactive", "--json")
	if _, err := os.Stat(filepath.Join(work, ".beads")); !os.IsNotExist(err) {
		t.Fatalf("preflight created graph storage: %v", err)
	}
	graphPolicyCLI(t, bd, work, home, nil, "", "init", "--graph-mode", "link", "--scope-url", "https://example.invalid/auto-stop/", "--skip-hooks", "--non-interactive", "--json")
	raw, err := os.ReadFile(settings)
	if err != nil || string(raw) != `{"hooks":{"Stop":"not an array"}}` {
		t.Fatalf("skip-hooks changed existing settings: %v %q", err, raw)
	}
}

// This invokes the actual installed Stop registration and Steph's handler; it
// proves delivery and execution, not that an agent chooses useful facts to save.
func TestGraphPreviewClaudeStopWorkflow(t *testing.T) {
	bd := buildBDUnderTest(t)
	for _, engine := range []string{"embedded", "server"} {
		t.Run(engine, func(t *testing.T) {
			work, home := t.TempDir(), t.TempDir()
			write := func(path, content string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			read := func(path string) []byte {
				t.Helper()
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				return raw
			}
			write(filepath.Join(work, "AGENTS.md"), "User instructions remain.\n")
			args := []string{"init", "--graph-mode", "link", "--scope-url", "https://example.invalid/stop/", "--skip-hooks", "--non-interactive", "--json"}
			if engine == "server" {
				port := os.Getenv("BEADS_GRAPH_TEST_SERVER_PORT")
				if port == "" {
					t.Skip("set BEADS_GRAPH_TEST_SERVER_PORT for ordinary shared-server CLI qualification")
				}
				args = append(args, "--server", "--external", "--server-host", "127.0.0.1", "--server-port", port, "--server-user", "root")
			}
			graphPolicyCLI(t, bd, work, home, nil, "", args...)
			settings := filepath.Join(work, ".claude", "settings.json")
			if _, err := os.Stat(settings); !os.IsNotExist(err) {
				t.Fatal("init implicitly installed hook")
			}
			beforeAgents := read(filepath.Join(work, "AGENTS.md"))
			if _, err := os.Stat(filepath.Join(work, "CLAUDE.md")); !os.IsNotExist(err) {
				t.Fatal("init implicitly installed Claude import")
			}
			write(settings, `{"permissions":{"allow":["Read"]},"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"user-start"}]}],"Stop":[{"hooks":[{"type":"command","command":"user-stop"}]}]}}`)
			call := func(args ...string) string { t.Helper(); return graphPolicyCLI(t, bd, work, home, nil, "", args...) }
			call("setup", "claude")
			beforeClaude := read(filepath.Join(work, "CLAUDE.md"))
			if string(beforeClaude) != "@AGENTS.md\n" {
				t.Fatalf("fresh setup did not load graph guidance: %q", beforeClaude)
			}
			installed := read(settings)
			if info, err := os.Stat(settings); err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("setup widened settings permissions: %v %v", info, err)
			}
			call("setup", "Claude", "--project")
			call("setup", "claude", "--check")
			if !bytes.Equal(installed, read(settings)) || !bytes.Equal(beforeAgents, read(filepath.Join(work, "AGENTS.md"))) || !bytes.Equal(beforeClaude, read(filepath.Join(work, "CLAUDE.md"))) {
				t.Fatal("setup changed user instructions or was not idempotent")
			}
			if bytes.Contains(installed, []byte("bd prime")) || !bytes.Contains(installed, []byte("user-start")) || !bytes.Contains(installed, []byte("user-stop")) || !bytes.Contains(installed, []byte("permissions")) {
				t.Fatal("setup lost siblings or installed prime")
			}
			var configured struct {
				Hooks map[string][]struct {
					Hooks []struct{ Type, Command string }
				}
			}
			if err := json.Unmarshal(installed, &configured); err != nil {
				t.Fatal(err)
			}
			var command []string
			for _, entry := range configured.Hooks["Stop"] {
				for _, hook := range entry.Hooks {
					if hook.Command == "bd claude-hook stop" && hook.Type == "command" {
						if command != nil {
							t.Fatal("duplicate Stop registration")
						}
						command = strings.Fields(hook.Command)
					}
				}
			}
			if len(command) != 3 {
				t.Fatal("missing actual Stop command")
			}
			// Seed a record, then make the database unavailable during the hook.
			original := call("remember", "Existing fact", "--json")
			memory := graphMixedResult[graphstore.Record](t, original)
			transcript := filepath.Join(work, "session.jsonl")
			write(transcript, "{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"tool_use\",\"id\":\"tool-1\",\"name\":\"Read\",\"input\":{}}]}}\n")
			beadsDir := filepath.Join(work, ".beads")
			metadata := filepath.Join(beadsDir, "metadata.json")
			originalMetadata := read(metadata)
			var hookEnv []string
			if engine == "server" {
				cfg, err := configfile.Load(beadsDir)
				if err != nil {
					t.Fatal(err)
				}
				cfg.DoltServerPort = 1
				if err := cfg.Save(beadsDir); err != nil {
					t.Fatal(err)
				}
				hookEnv = []string{"BEADS_DOLT_SERVER_PORT=1"}
			} else {
				if err := os.Rename(filepath.Join(beadsDir, "embeddeddolt"), filepath.Join(beadsDir, "embeddeddolt-unavailable")); err != nil {
					t.Fatal(err)
				}
			}
			restored := false
			restore := func() {
				if restored {
					return
				}
				restored = true
				if engine == "embedded" {
					if err := os.Rename(filepath.Join(beadsDir, "embeddeddolt-unavailable"), filepath.Join(beadsDir, "embeddeddolt")); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(metadata, originalMetadata, 0600); err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(restore)
			runStop := func(session string, active bool) string {
				t.Helper()
				input, _ := json.Marshal(claudeHookInput{SessionID: session, TranscriptPath: transcript, CWD: work, HookEventName: "Stop", StopHookActive: active})
				ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
				defer cancel()
				process := graphMemoryReadCommand(ctx, bd, work, home, command[1:]...)
				process.Env = append(process.Env, hookEnv...)
				process.Stdin = bytes.NewReader(input)
				var stdout, stderr bytes.Buffer
				process.Stdout, process.Stderr = &stdout, &stderr
				if err := process.Run(); err != nil || ctx.Err() != nil || stderr.Len() != 0 {
					t.Fatalf("store-free Stop failed: %v timeout=%v stderr=%s", err, ctx.Err(), stderr.String())
				}
				return stdout.String()
			}
			var decision claudeStopDecision
			if err := json.Unmarshal([]byte(runStop("first", false)), &decision); err != nil || decision.Decision != "block" || decision.Reason != claudeStopMemoryReminder {
				t.Fatalf("missing original reminder: %+v %v", decision, err)
			}
			if got := runStop("first", false); got != "" {
				t.Fatalf("repeated Stop reminded again: %s", got)
			}
			if got := runStop("reentrant", true); got != "" {
				t.Fatalf("reentrant Stop reminded: %s", got)
			}
			restore()
			if call("show", memory.ID, "--json") != original {
				t.Fatal("hook changed stored Memory")
			}
			remembered := graphMixedResult[graphstore.Record](t, call("remember", "Keep integration work on integration", "--json"))
			graphMemoryReadRaw(t, bd, work, home, "Keep integration work on integration", "", "recall", remembered.ID)
			for _, flags := range [][]string{{"--global"}, {"--stealth"}, {"--print"}, {"--check", "--remove"}, {"--project=false"}} {
				code := "capability_unavailable"
				if flags[0] == "--check" || flags[0] == "--project=false" {
					code = "invalid_selector"
				}
				graphPolicyCLI(t, bd, work, home, nil, code, append([]string{"setup", "claude", "--json"}, flags...)...)
			}
			graphPolicyCLI(t, bd, work, home, nil, "capability_unavailable", "setup", "claude", "--json")
			graphPolicyCLI(t, bd, work, home, nil, "capability_unavailable", "setup", "codex", "--json")
			call("setup", "claude", "--remove")
			removed := read(settings)
			graphSetupRefusal(t, bd, work, home, "not installed", "setup", "claude", "--check")
			call("setup", "claude", "--remove")
			if !bytes.Equal(beforeClaude, read(filepath.Join(work, "CLAUDE.md"))) {
				t.Fatal("remove changed guidance import")
			}
			if bytes.Contains(removed, []byte("bd claude-hook stop")) || !bytes.Equal(removed, read(settings)) || !bytes.Contains(removed, []byte("user-stop")) || !bytes.Equal(beforeAgents, read(filepath.Join(work, "AGENTS.md"))) {
				t.Fatal("remove lost user content or was not idempotent")
			}
			// Existing user content gets one import, then survives install/remove.
			write(filepath.Join(work, "CLAUDE.md"), "User Claude instructions remain.")
			if err := os.Chmod(filepath.Join(work, "CLAUDE.md"), 0600); err != nil {
				t.Fatal(err)
			}
			call("setup", "claude")
			if info, err := os.Stat(filepath.Join(work, "CLAUDE.md")); err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("setup widened CLAUDE permissions: %v %v", info, err)
			}
			wantClaude := "User Claude instructions remain.\n\n@AGENTS.md\n"
			if got := string(read(filepath.Join(work, "CLAUDE.md"))); got != wantClaude {
				t.Fatalf("existing instructions %q", got)
			}
			call("setup", "claude")
			call("setup", "claude", "--check")
			call("setup", "claude", "--remove")
			if got := string(read(filepath.Join(work, "CLAUDE.md"))); got != wantClaude {
				t.Fatal("repeat install/remove changed user instructions")
			}
		})
	}
}

func graphSetupRefusal(t *testing.T, bd, work, home, fragment string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := graphMemoryReadCommand(ctx, bd, work, home, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if ctx.Err() != nil || !errors.As(err, &exit) || exit.ExitCode() != 5 || stdout.Len() != 0 || !strings.HasPrefix(stderr.String(), "capability_unavailable:") || !strings.Contains(stderr.String(), fragment) {
		t.Fatalf("setup refusal %v: err=%v timeout=%v stdout=%q stderr=%q", args, err, ctx.Err(), stdout.String(), stderr.String())
	}
}

func TestGraphPreviewClaudeStopProfileRefusal(t *testing.T) {
	bd := buildBDUnderTest(t)
	for _, engine := range []string{"embedded", "server"} {
		t.Run(engine, func(t *testing.T) {
			work, home := t.TempDir(), t.TempDir()
			args := []string{"init", "--graph-mode", "link", "--scope-url", "https://example.invalid/stop-profile/", "--skip-hooks", "--skip-agents", "--non-interactive", "--json"}
			if engine == "server" {
				port := os.Getenv("BEADS_GRAPH_TEST_SERVER_PORT")
				if port == "" {
					t.Skip("set BEADS_GRAPH_TEST_SERVER_PORT for ordinary shared-server CLI qualification")
				}
				args = append(args, "--server", "--external", "--server-host", "127.0.0.1", "--server-port", port, "--server-user", "root")
			}
			graphPolicyCLI(t, bd, work, home, nil, "", args...)
			settings, claude := filepath.Join(work, ".claude", "settings.json"), filepath.Join(work, "CLAUDE.md")
			if err := os.MkdirAll(filepath.Dir(settings), 0755); err != nil {
				t.Fatal(err)
			}
			const userSettings = `{"user_setting":true}`
			const userClaude = "User instructions\n@AGENTS.md\n"
			if err := os.WriteFile(settings, []byte(userSettings), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(claude, []byte(userClaude), 0600); err != nil {
				t.Fatal(err)
			}
			stale := strings.Replace(agents.RenderSection(agents.ProfileGraphPreview), "hash:"+agents.CurrentHash(agents.ProfileGraphPreview), "hash:00000000", 1)
			for _, tc := range []struct{ name, content, fragment string }{
				{"missing", "", "requires existing graph-preview"},
				{"minimal", agents.RenderSection(agents.ProfileMinimal), "requires existing graph-preview"},
				{"stale", stale, "stale managed hash"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					path := filepath.Join(work, "AGENTS.md")
					if tc.content != "" {
						if err := os.WriteFile(path, []byte(tc.content), 0600); err != nil {
							t.Fatal(err)
						}
					}
					for _, suffix := range [][]string{nil, {"--check"}} {
						graphSetupRefusal(t, bd, work, home, tc.fragment, append([]string{"setup", "claude"}, suffix...)...)
						for path, want := range map[string]string{settings: userSettings, claude: userClaude} {
							got, err := os.ReadFile(path)
							if err != nil || string(got) != want {
								t.Fatalf("refusal changed %s: %q %v", path, got, err)
							}
						}
						got, err := os.ReadFile(path)
						if tc.content == "" {
							if !os.IsNotExist(err) {
								t.Fatalf("refusal created missing instructions: %v", err)
							}
						} else if err != nil || string(got) != tc.content {
							t.Fatal("refusal refreshed or changed instructions")
						}
					}
				})
			}
		})
	}
}

// graphSetupRun runs one isolated bd process and returns its exit code,
// stdout and stderr, so a test can assert warnings on a successful exit.
func graphSetupRun(t *testing.T, bd, work, home string, extraEnv []string, stdin []byte, args ...string) (int, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	cmd := graphMemoryReadCommand(ctx, bd, work, home, args...)
	cmd.Env = append(cmd.Env, extraEnv...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	cleanupGraphTestServer(t, work, args)
	if ctx.Err() != nil {
		t.Fatalf("bd %v exceeded its deadline: %v stderr=%s", args, ctx.Err(), stderr.String())
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), stdout.String(), stderr.String()
	}
	if err != nil {
		t.Fatalf("bd %v did not run: %v", args, err)
	}
	return 0, stdout.String(), stderr.String()
}

func graphSetupInitArgs(t *testing.T, engine, scope string, flags ...string) []string {
	t.Helper()
	args := append([]string{"init", "--graph-mode", "link", "--scope-url", scope, "--non-interactive", "--json"}, flags...)
	if engine == "server" {
		port := os.Getenv("BEADS_GRAPH_TEST_SERVER_PORT")
		if port == "" {
			t.Skip("set BEADS_GRAPH_TEST_SERVER_PORT for ordinary shared-server CLI qualification")
		}
		args = append(args, "--server", "--external", "--server-host", "127.0.0.1", "--server-port", port, "--server-user", "root")
	}
	return args
}

// Once graph storage exists, a Stop hook that cannot be written must not leave
// a fenced workspace that neither init nor setup can repair: init warns,
// publishes the workspace as ready, and bd setup claude adds the hook after the
// cause is fixed.
func TestGraphPreviewClaudeStopInstallFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the read-only .claude directory mode this test uses to make the hook write fail")
	}
	bd := buildBDUnderTest(t)
	for _, engine := range []string{"embedded", "server"} {
		t.Run(engine, func(t *testing.T) {
			work, home := t.TempDir(), t.TempDir()
			args := graphSetupInitArgs(t, engine, "https://example.invalid/stop-install-failure/")
			claudeDir := filepath.Join(work, ".claude")
			if err := os.Mkdir(claudeDir, 0o755); err != nil {
				t.Fatal(err)
			}
			// Preflight only reads this directory; the later settings write fails.
			if err := os.Chmod(claudeDir, 0o555); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(claudeDir, 0o755) })
			code, stdout, stderr := graphSetupRun(t, bd, work, home, nil, nil, args...)
			if code != 0 {
				t.Fatalf("hook write failure aborted init: exit=%d stderr=%s", code, stderr)
			}
			if initialized := graphMixedResult[map[string]any](t, stdout); initialized["backend"] != engine {
				t.Fatalf("unexpected initialized engine: %+v", initialized)
			}
			if !strings.HasPrefix(stderr, "Warning: ") || !strings.Contains(stderr, filepath.Join(claudeDir, "settings.json")) || !strings.Contains(stderr, "Run `bd setup claude` after fixing this to add the Stop hook") {
				t.Fatalf("init did not warn about the missing Stop hook: %q", stderr)
			}
			// Admission requires a published, ready workspace.
			graphPolicyCLI(t, bd, work, home, nil, "", "status", "--graph", "--json")
			if _, err := os.Lstat(filepath.Join(claudeDir, "settings.json")); !os.IsNotExist(err) {
				t.Fatalf("failed write left settings behind: %v", err)
			}
			graphSetupRefusal(t, bd, work, home, "bd setup claude", "setup", "claude", "--check")
			if err := os.Chmod(claudeDir, 0o755); err != nil {
				t.Fatal(err)
			}
			graphPolicyCLI(t, bd, work, home, nil, "", "setup", "claude")
			graphPolicyCLI(t, bd, work, home, nil, "", "setup", "claude", "--check")
			settings, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
			if err != nil || bytes.Count(settings, []byte("bd claude-hook stop")) != 1 {
				t.Fatalf("setup did not register one Stop hook: %v %s", err, settings)
			}
			if claude, err := os.ReadFile(filepath.Join(work, "CLAUDE.md")); err != nil || string(claude) != "@AGENTS.md\n" {
				t.Fatalf("setup did not import graph guidance: %v %q", err, claude)
			}
		})
	}
}

// --skip-agents alone (without --skip-hooks) omits guidance, the CLAUDE.md
// import and the Stop hook, so it also skips the hook preflight: a globally
// enabled Beads plugin, which refuses a default init, does not refuse this one,
// and no hook installation is attempted (it would warn on stderr).
func TestGraphPreviewClaudeStopSkipAgentsAlone(t *testing.T) {
	bd := buildBDUnderTest(t)
	for _, engine := range []string{"embedded", "server"} {
		t.Run(engine, func(t *testing.T) {
			work, home := t.TempDir(), t.TempDir()
			args := graphSetupInitArgs(t, engine, "https://example.invalid/skip-agents-alone/", "--skip-agents")
			global := filepath.Join(home, ".claude", "settings.json")
			const plugin = `{"enabledPlugins":{"beads@example":true}}`
			if err := os.MkdirAll(filepath.Dir(global), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(global, []byte(plugin), 0o600); err != nil {
				t.Fatal(err)
			}
			code, stdout, stderr := graphSetupRun(t, bd, work, home, nil, nil, args...)
			if code != 0 || stderr != "" {
				t.Fatalf("--skip-agents init: exit=%d stderr=%q", code, stderr)
			}
			if initialized := graphMixedResult[map[string]any](t, stdout); initialized["backend"] != engine {
				t.Fatalf("unexpected initialized engine: %+v", initialized)
			}
			for _, name := range []string{"AGENTS.md", "CLAUDE.md", ".claude"} {
				if _, err := os.Lstat(filepath.Join(work, name)); !os.IsNotExist(err) {
					t.Fatalf("--skip-agents alone created %s: %v", name, err)
				}
			}
			if raw, err := os.ReadFile(global); err != nil || string(raw) != plugin {
				t.Fatalf("--skip-agents changed global settings: %v %q", err, raw)
			}
			graphPolicyCLI(t, bd, work, home, nil, "", "status", "--graph", "--json")
		})
	}
}

// Default init still refuses known Claude conflicts before creating storage,
// but each refusal names the file to change and the --skip-hooks alternative,
// which then initializes without touching that file.
func TestGraphPreviewClaudeStopAutoInitRefusalMessages(t *testing.T) {
	bd := buildBDUnderTest(t)
	write := func(t *testing.T, path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name string
		// arrange returns the file the refusal must name and one it must not.
		arrange func(t *testing.T, work, home string) (culprit, innocent string)
	}{
		{"global-plugin", func(t *testing.T, work, home string) (string, string) {
			// The project disables the plugin, but the global file still enables it.
			global, project := filepath.Join(home, ".claude", "settings.json"), filepath.Join(work, ".claude", "settings.json")
			write(t, global, `{"enabledPlugins":{"beads@example":true}}`)
			write(t, project, `{"enabledPlugins":{"beads@example":false}}`)
			return global, project
		}},
		{"claude-symlink", func(t *testing.T, work, home string) (string, string) {
			claude := filepath.Join(work, "CLAUDE.md")
			if err := os.Symlink("AGENTS.md", claude); err != nil {
				t.Fatal(err)
			}
			return claude, ""
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			work, home := t.TempDir(), t.TempDir()
			culprit, innocent := tc.arrange(t, work, home)
			snapshot := func() string {
				t.Helper()
				if target, err := os.Readlink(culprit); err == nil {
					return "symlink to " + target
				}
				raw, err := os.ReadFile(culprit)
				if err != nil {
					t.Fatal(err)
				}
				return string(raw)
			}
			before := snapshot()
			args := []string{"init", "--graph-mode", "link", "--scope-url", "https://example.invalid/auto-stop-refusal/", "--non-interactive", "--json"}
			code, stdout, stderr := graphSetupRun(t, bd, work, home, nil, nil, args...)
			var refusal struct{ Code, Message string }
			if err := json.Unmarshal([]byte(stderr), &refusal); err != nil || code != 5 || stdout != "" || refusal.Code != "graph_not_initialized" {
				t.Fatalf("expected a typed preflight refusal: exit=%d stdout=%q stderr=%q err=%v", code, stdout, stderr, err)
			}
			for _, want := range []string{culprit, "`bd init --graph-mode link --skip-hooks`", "`bd setup claude` can add the hook"} {
				if !strings.Contains(refusal.Message, want) {
					t.Fatalf("refusal omits %q: %s", want, refusal.Message)
				}
			}
			if innocent != "" && strings.Contains(refusal.Message, innocent) {
				t.Fatalf("refusal blames %s, which disables the plugin: %s", innocent, refusal.Message)
			}
			if _, err := os.Lstat(filepath.Join(work, ".beads")); !os.IsNotExist(err) {
				t.Fatalf("preflight refusal created graph storage: %v", err)
			}
			graphPolicyCLI(t, bd, work, home, nil, "", append(args, "--skip-hooks")...)
			if after := snapshot(); after != before {
				t.Fatalf("--skip-hooks init changed %s: %q -> %q", culprit, before, after)
			}
		})
	}
}

// Init is the one command that accepts --skip-agents, and with it the refused
// file is left untouched, so init keeps offering it. The refusal comes from
// preflight, before any graph storage exists.
func TestGraphPreviewInitAgentFileRefusalsOfferSkipAgents(t *testing.T) {
	bd := buildBDUnderTest(t)
	for _, tc := range graphPreviewAgentRefusals {
		t.Run(tc.name, func(t *testing.T) {
			work, home := t.TempDir(), t.TempDir()
			tc.arrange(t, filepath.Join(work, "AGENTS.md"))
			args := []string{"init", "--graph-mode", "link", "--scope-url", "https://example.invalid/agent-refusal/", "--skip-hooks", "--non-interactive", "--json"}
			code, stdout, stderr := graphSetupRun(t, bd, work, home, nil, nil, args...)
			var refusal struct{ Code, Message string }
			if err := json.Unmarshal([]byte(stderr), &refusal); err != nil || code != 5 || stdout != "" || refusal.Code != "graph_not_initialized" {
				t.Fatalf("expected a typed preflight refusal: exit=%d stdout=%q stderr=%q err=%v", code, stdout, stderr, err)
			}
			for _, want := range []string{"agent guidance was not installed; no graph database was initialized: agent file AGENTS.md", tc.problem} {
				if !strings.Contains(refusal.Message, want) {
					t.Errorf("refusal omits %q: %s", want, refusal.Message)
				}
			}
			if !strings.HasSuffix(refusal.Message, "; preserve it with --skip-agents") {
				t.Errorf("init refusal does not end with its --skip-agents step: %s", refusal.Message)
			}
			if _, err := os.Lstat(filepath.Join(work, ".beads")); !os.IsNotExist(err) {
				t.Fatalf("preflight refusal created graph storage: %v", err)
			}
		})
	}
}

// Claude Code reads exit 2 from a Stop hook as "block stopping", and graph
// admission runs before the reminder's stop_hook_active guard. Admission
// refusals in the hook (BD_BACKEND selectors, a moved workspace) must warn on
// one line and exit 1, as an ordinary workspace does for BD_BACKEND, while the
// reminder itself keeps its JSON block decision once admission succeeds.
func TestGraphPreviewClaudeStopHookAdmissionNonBlocking(t *testing.T) {
	bd := buildBDUnderTest(t)
	for _, engine := range []string{"embedded", "server"} {
		t.Run(engine, func(t *testing.T) {
			root, home := t.TempDir(), t.TempDir()
			work := filepath.Join(root, "work")
			if err := os.Mkdir(work, 0o755); err != nil {
				t.Fatal(err)
			}
			graphPolicyCLI(t, bd, work, home, nil, "", graphSetupInitArgs(t, engine, "https://example.invalid/stop-admission/")...)
			transcript := filepath.Join(root, "session.jsonl")
			if err := os.WriteFile(transcript, []byte("{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"tool_use\",\"id\":\"tool-1\",\"name\":\"Read\",\"input\":{}}]}}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			stop := func(dir string, env []string) (int, string, string) {
				t.Helper()
				input, _ := json.Marshal(claudeHookInput{SessionID: "session", TranscriptPath: transcript, CWD: dir, HookEventName: "Stop"})
				return graphSetupRun(t, bd, dir, home, env, input, "claude-hook", "stop")
			}
			refused := func(name, want string, code int, stdout, stderr string) {
				t.Helper()
				if code != 1 || stdout != "" || !strings.HasPrefix(stderr, "Warning: ") || strings.Count(stderr, "\n") != 1 || !strings.HasSuffix(stderr, "\n") || !strings.Contains(stderr, want) {
					t.Fatalf("%s: want one warning line naming %q and exit 1: exit=%d stdout=%q stderr=%q", name, want, code, stdout, stderr)
				}
			}
			code, stdout, stderr := stop(work, []string{"BD_BACKEND=sqlite"})
			refused("environment", "BD_BACKEND", code, stdout, stderr)
			envFile := filepath.Join(work, ".beads", ".env")
			if err := os.WriteFile(envFile, []byte("BD_DATABASE_BACKEND=sqlite\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			code, stdout, stderr = stop(work, nil)
			var decision claudeStopDecision
			if err := json.Unmarshal([]byte(stdout), &decision); err != nil || code != 0 || decision.Decision != "block" || decision.Reason != claudeStopMemoryReminder || !strings.Contains(stderr, "ignored unsupported project .env key") {
				t.Fatalf("project backend setting must stay ignored without disabling the reminder: %d %q %q", code, stdout, stderr)
			}
			if err := os.Remove(envFile); err != nil {
				t.Fatal(err)
			}
			moved := filepath.Join(root, "moved")
			if err := os.Rename(work, moved); err != nil {
				t.Fatal(err)
			}
			code, stdout, stderr = stop(moved, nil)
			refused("moved workspace", "moved", code, stdout, stderr)
			if err := os.Rename(moved, work); err != nil {
				t.Fatal(err)
			}
			// The ignored project setting already emitted the reminder. Repeating
			// Stop without new tool use must not emit a second block decision.
			code, stdout, stderr = stop(work, nil)
			if code != 0 || stdout != "" || stderr != "" {
				t.Fatalf("admitted Stop repeated its reminder: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
		})
	}
}
