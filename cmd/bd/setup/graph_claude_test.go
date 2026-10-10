package setup

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGraphPreviewClaudeStopSettings(t *testing.T) {
	env, _, _ := newClaudeTestEnv(t)
	path := projectSettingsPath(env.projectDir)
	writeSettings(t, path, map[string]interface{}{
		"userSetting": "preserved",
		"hooks": map[string]interface{}{
			"Stop":         []interface{}{map[string]interface{}{"hooks": []interface{}{map[string]interface{}{"type": "command", "command": "user stop"}}}},
			"SessionStart": []interface{}{map[string]interface{}{"hooks": []interface{}{map[string]interface{}{"type": "command", "command": "user start"}}}},
		},
	})
	if err := graphClaudeStop(env, false, false); err != nil {
		t.Fatal(err)
	}
	installed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := graphClaudeStop(env, false, false); err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(path)
	againInfo, _ := os.Stat(path)
	if !bytes.Equal(installed, again) || !info.ModTime().Equal(againInfo.ModTime()) {
		t.Fatal("repeat setup rewrote settings")
	}
	if err := graphClaudeStop(env, true, false); err != nil {
		t.Fatal(err)
	}
	if err := graphClaudeStop(env, false, true); err != nil {
		t.Fatal(err)
	}
	removed, _ := os.ReadFile(path)
	if bytes.Contains(removed, []byte(claudeStopHookCommand)) || !bytes.Contains(removed, []byte("user stop")) || !bytes.Contains(removed, []byte("user start")) || !bytes.Contains(removed, []byte("preserved")) {
		t.Fatal("removal lost user settings or retained managed Stop")
	}
	if err := graphClaudeStop(env, false, true); err != nil {
		t.Fatal(err)
	}
	again, _ = os.ReadFile(path)
	if !bytes.Equal(removed, again) {
		t.Fatal("repeat remove rewrote settings")
	}
	if err := graphClaudeStop(env, true, false); err == nil {
		t.Fatal("check accepted missing Stop hook")
	}
}

func TestGraphPreviewClaudeStopRefusalPreservesSettings(t *testing.T) {
	for _, tc := range []struct{ name, where, raw string }{
		{"project-prime", "project", `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"bd prime --hook-json"}]}]}}`},
		{"legacy-prime", "legacy", `{"hooks":{"PreCompact":[{"hooks":[{"type":"command","command":"bd prime"}]}]}}`},
		{"global-prime", "global", `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"bd prime --stealth --hook-json"}]}]}}`},
		{"project-plugin", "project", `{"enabledPlugins":{"beads@example":true}}`},
		{"global-plugin", "global", `{"enabledPlugins":{"beads@example":true}}`},
		{"project-overflow-only", "project", `{"unrelated":1e400}`},
		{"project-overflow-plugin", "project", `{"unrelated":1e400,"enabledPlugins":{"beads@example":true}}`},
		{"project-overflow-prime", "project", `{"unrelated":1e400,"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"bd prime --hook-json"}]}]}}`},
		{"global-overflow-only", "global", `{"unrelated":1e400}`},
		{"global-overflow-plugin", "global", `{"unrelated":1e400,"enabledPlugins":{"beads@example":true}}`},
		{"global-overflow-prime", "global", `{"unrelated":1e400,"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"bd prime --hook-json"}]}]}}`},
		{"legacy-overflow-only", "legacy", `{"unrelated":1e400}`},
		{"legacy-overflow-plugin", "legacy", `{"unrelated":1e400,"enabledPlugins":{"beads@example":true}}`},
		{"legacy-overflow-prime", "legacy", `{"unrelated":1e400,"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"bd prime --hook-json"}]}]}}`},
		{"null-settings", "project", `null`},
		{"trailing-settings", "project", `{} {}`},
		{"bad-hooks", "project", `{"hooks":null}`},
		{"bad-stop", "project", `{"hooks":{"Stop":{}}}`},
		{"bad-stop-entry", "project", `{"hooks":{"Stop":[{"hooks":null}]}}`},
		{"conditional-stop", "project", `{"hooks":{"Stop":[{"matcher":"other","hooks":[{"type":"command","command":"bd claude-hook stop"}]}]}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, _, _ := newClaudeTestEnv(t)
			path := projectSettingsPath(env.projectDir)
			if tc.where == "legacy" {
				path = legacyProjectSettingsPath(env.projectDir)
			}
			if tc.where == "global" {
				path = globalSettingsPath(env.homeDir)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tc.raw), 0600); err != nil {
				t.Fatal(err)
			}
			claude := filepath.Join(env.projectDir, claudeInstructionsFile)
			const instructions = "User instructions without a graph import\n"
			if err := os.WriteFile(claude, []byte(instructions), 0600); err != nil {
				t.Fatal(err)
			}
			for _, check := range []bool{false, true} {
				err := graphClaudeStop(env, check, false)
				if err == nil {
					t.Fatal("unsupported setup accepted")
				}
				if strings.Contains(tc.name, "overflow") && !strings.Contains(err.Error(), "unsupported JSON or numeric range") {
					t.Fatalf("overflow did not refuse before hook detection: %v", err)
				}
				got, err := os.ReadFile(path)
				if err != nil || string(got) != tc.raw {
					t.Fatal("refusal changed settings")
				}
				got, err = os.ReadFile(claude)
				if err != nil || string(got) != instructions {
					t.Fatal("refusal changed instructions/import")
				}
				if tc.where != "project" {
					if _, err := os.Stat(projectSettingsPath(env.projectDir)); !os.IsNotExist(err) {
						t.Fatal("refusal created project settings")
					}
				}
			}
		})
	}
	t.Run("symlink", func(t *testing.T) {
		env, _, _ := newClaudeTestEnv(t)
		outside := filepath.Join(t.TempDir(), "outside.json")
		if err := os.WriteFile(outside, []byte(`{"user":true}`), 0600); err != nil {
			t.Fatal(err)
		}
		path := projectSettingsPath(env.projectDir)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, path); err != nil {
			t.Fatal(err)
		}
		if err := graphClaudeStop(env, false, false); err == nil {
			t.Fatal("symlink settings accepted")
		}
		got, _ := os.ReadFile(outside)
		if string(got) != `{"user":true}` {
			t.Fatal("outside target mutated")
		}
	})
}

func TestGraphPreviewClaudeGuidanceImport(t *testing.T) {
	for _, tc := range []struct {
		name, before, want string
		refusal            bool
	}{
		{"fresh", "", "@AGENTS.md\n", false},
		{"user-text", "User rules", "User rules\n\n@AGENTS.md\n", false},
		{"active-import", "@./AGENTS.md\nUser rules\n", "@./AGENTS.md\nUser rules\n", false},
		{"fenced-example", "```\n@AGENTS.md\n```\n", "```\n@AGENTS.md\n```\n\n@AGENTS.md\n", false},
		{"unclosed-fence", "```\nExample\n", "", true},
		{"stale-profile", "<!-- BEGIN BEADS INTEGRATION -->\nStale instructions\n<!-- END BEADS INTEGRATION -->\n", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, _, _ := newClaudeTestEnv(t)
			path := filepath.Join(env.projectDir, claudeInstructionsFile)
			if tc.before != "" {
				if err := os.WriteFile(path, []byte(tc.before), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := graphClaudeStop(env, false, false)
			if tc.refusal {
				if err == nil {
					t.Fatal("unsafe instructions admitted")
				}
				got, _ := os.ReadFile(path)
				if string(got) != tc.before {
					t.Fatal("refusal changed instructions")
				}
				if _, err := os.Stat(projectSettingsPath(env.projectDir)); !os.IsNotExist(err) {
					t.Fatal("refusal published settings")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, op := range []struct{ check, remove bool }{{false, false}, {true, false}, {false, true}} {
				if err := graphClaudeStop(env, op.check, op.remove); err != nil {
					t.Fatal(err)
				}
				got, _ := os.ReadFile(path)
				if string(got) != tc.want {
					t.Fatalf("instructions %q want %q", got, tc.want)
				}
			}
		})
	}
	t.Run("unsafe-import-path", func(t *testing.T) {
		env, _, _ := newClaudeTestEnv(t)
		outside := filepath.Join(t.TempDir(), "outside.md")
		if err := os.WriteFile(outside, []byte("User rules"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(env.projectDir, claudeInstructionsFile)); err != nil {
			t.Fatal(err)
		}
		if err := graphClaudeStop(env, false, false); err == nil {
			t.Fatal("symlink instructions admitted")
		}
		got, _ := os.ReadFile(outside)
		if string(got) != "User rules" {
			t.Fatal("outside instructions changed")
		}
		if _, err := os.Stat(projectSettingsPath(env.projectDir)); !os.IsNotExist(err) {
			t.Fatal("unsafe import published settings")
		}
	})
}

func TestGraphPreviewClaudeSettingsNumbersAndModes(t *testing.T) {
	env, _, _ := newClaudeTestEnv(t)
	path := projectSettingsPath(env.projectDir)
	claude := filepath.Join(env.projectDir, claudeInstructionsFile)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	const settings = `{"large":9007199254740993,"nested":{"huge":18446744073709551615,"fraction":1.0000000000000000001}}`
	if err := os.WriteFile(path, []byte(settings), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claude, []byte("User rules\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, remove := range []bool{false, false, true} {
		if err := graphClaudeStop(env, false, remove); err != nil {
			t.Fatal(err)
		}
		got, _, err := graphClaudeSettings(env, path)
		if err != nil {
			t.Fatal(err)
		}
		if got["large"] != json.Number("9007199254740993") || got["nested"].(map[string]interface{})["huge"] != json.Number("18446744073709551615") || got["nested"].(map[string]interface{})["fraction"] != json.Number("1.0000000000000000001") {
			t.Fatalf("numeric settings changed: %#v", got)
		}
		for _, file := range []string{path, claude} {
			info, err := os.Stat(file)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("mode widened for %s: %v %v", file, info, err)
			}
		}
	}
}

// Every init-time preflight refusal names the file to change and how to
// initialize without the hook. The plugin refusal lists each enabling file:
// hasBeadsPlugin accepts any enabling file, so a false entry elsewhere (here,
// the project) does not satisfy it and must not be named as the culprit.
func TestGraphPreviewClaudeStopPreflightRefusalMessages(t *testing.T) {
	const (
		enabled  = `{"enabledPlugins":{"beads@example":true}}`
		disabled = `{"enabledPlugins":{"beads@example":false}}`
		prime    = `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"bd prime --hook-json"}]}]}}`
	)
	// want[0] is the file that causes the refusal; {name} expands to its path.
	for _, tc := range []struct {
		name                            string
		project, legacy, global, claude string
		symlinkClaude                   bool
		want, absent                    []string
	}{
		{name: "global-plugin-project-disabled", project: disabled, global: enabled,
			want: []string{"{global}", "disable the plugin in that file", "enabledPlugins"}, absent: []string{"{project}", "{legacy}"}},
		{name: "project-and-global-plugin", project: enabled, global: enabled,
			want: []string{"{project} and {global}", "each of those files"}, absent: []string{"{legacy}"}},
		{name: "legacy-plugin", legacy: enabled, want: []string{"{legacy}", "disable the plugin in that file"}},
		{name: "global-prime", global: prime, want: []string{"{global}", "bd prime SessionStart/PreCompact hook entries"}},
		{name: "global-unparseable", global: `{"enabledPlugins":`, want: []string{"{global}", "unsupported JSON or numeric range", "fix or remove"}},
		{name: "malformed-stop", project: `{"hooks":{"Stop":"not an array"}}`, want: []string{"{project}", "hooks.Stop must be an array"}},
		{name: "claude-symlink", symlinkClaude: true, want: []string{"{claude}", "replace it with a regular file", "@AGENTS.md"}},
		{name: "claude-managed-block", claude: "<!-- BEGIN BEADS INTEGRATION -->\nStale\n<!-- END BEADS INTEGRATION -->\n", want: []string{"{claude}", "remove that block"}},
		{name: "claude-unclosed-fence", claude: "```\nExample\n", want: []string{"{claude}", "close the fence"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, _, _ := newClaudeTestEnv(t)
			paths := map[string]string{
				"{project}": projectSettingsPath(env.projectDir),
				"{legacy}":  legacyProjectSettingsPath(env.projectDir),
				"{global}":  globalSettingsPath(env.homeDir),
				"{claude}":  filepath.Join(env.projectDir, claudeInstructionsFile),
			}
			expand := func(s string) string {
				for name, path := range paths {
					s = strings.ReplaceAll(s, name, path)
				}
				return s
			}
			for name, content := range map[string]string{"{project}": tc.project, "{legacy}": tc.legacy, "{global}": tc.global, "{claude}": tc.claude} {
				if content == "" {
					continue
				}
				if err := os.MkdirAll(filepath.Dir(paths[name]), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(paths[name], []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.symlinkClaude {
				if err := os.Symlink("AGENTS.md", paths["{claude}"]); err != nil {
					t.Fatal(err)
				}
			}
			stubClaudeEnvProvider(t, env, nil)
			err := PreflightGraphClaudeStop(env.projectDir)
			if err == nil {
				t.Fatal("preflight accepted a conflicting setup")
			}
			for _, want := range append(tc.want, "`bd init --graph-mode link --skip-hooks`", "`bd setup claude` can add the hook") {
				if !strings.Contains(err.Error(), expand(want)) {
					t.Errorf("refusal omits %q:\n%v", expand(want), err)
				}
			}
			for _, unwanted := range tc.absent {
				if strings.Contains(err.Error(), expand(unwanted)) {
					t.Errorf("refusal names %s, which does not cause it:\n%v", expand(unwanted), err)
				}
			}
			// Explicit setup has no --skip-hooks; only init's preflight offers it.
			if setupErr := graphClaudeStop(env, false, false); setupErr == nil || strings.Contains(setupErr.Error(), "--skip-hooks") || !strings.Contains(setupErr.Error(), expand(tc.want[0])) {
				t.Errorf("explicit setup refusal: %v", setupErr)
			}
		})
	}
}
