//go:build cgo && unix

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Runs real CLI processes: pre-run config resolution must survive into the
// committed journal without changing created_by or the audit actor.
func TestActorSourceCLIJournal(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1")
	}
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "asp", "--skip-hooks", "--skip-agents")
	env := bdEnv(dir)
	for _, key := range []string{"BEADS_ACTOR", "BD_ACTOR", "USER", "BD_EVENTS_JOURNAL", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_NOSYSTEM"} {
		env = envWithout(env, key)
	}
	env = append(env, "BD_EVENTS_JOURNAL=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git("init", "-q")
	git("config", "core.hooksPath", filepath.Join(dir, "disabled-hooks"))
	git("config", "user.name", "OwnerFixture")
	cases := []struct {
		name, actor, source, envKey, configured string
		flags                                   []string
	}{
		{name: "git", actor: "OwnerFixture", source: "git"},
		{name: "flag", actor: "worker-flag", source: "flag", envKey: "BEADS_ACTOR=ignored", flags: []string{"--actor", "worker-flag"}},
		{name: "env", actor: "worker-env", source: "env", envKey: "BEADS_ACTOR=worker-env"},
		{name: "legacy-env", actor: "worker-legacy", source: "env", envKey: "BD_ACTOR=worker-legacy"},
		{name: "config", actor: "worker-config", source: "config", configured: "worker-config"},
		{name: "user", actor: "user-fixture", source: "user", envKey: "USER=user-fixture"},
		{name: "unknown", actor: "unknown", source: "unknown"},
	}
	configPath := filepath.Join(dir, ".beads", "config.yaml")
	original, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.source == "user" {
				git("config", "--unset", "user.name")
			}
			cfg := string(original)
			if tc.configured != "" {
				cfg += "\nactor: " + tc.configured + "\n"
			}
			if err := os.WriteFile(configPath, []byte(cfg), 0600); err != nil {
				t.Fatal(err)
			}
			callEnv := append([]string(nil), env...)
			if tc.envKey != "" {
				callEnv = append(callEnv, tc.envKey)
			}
			args := append([]string{"create", "work", "--silent"}, tc.flags...)
			id, _ := runBDOut(t, bd, dir, callEnv, args...)
			id = strings.TrimSpace(id)
			out, _ := runBDOut(t, bd, dir, env, "events", "export")
			var found bool
			for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
				var rec struct {
					Op     string `json:"op"`
					ID     string `json:"issue_id"`
					Actor  string `json:"actor"`
					Source string `json:"actor_source"`
					Issue  struct {
						CreatedBy string `json:"created_by"`
					} `json:"issue"`
				}
				if err := json.Unmarshal([]byte(line), &rec); err != nil {
					t.Fatal(err)
				}
				if rec.ID == id && rec.Op == "create" {
					found = true
					if rec.Actor != tc.actor || rec.Source != tc.source || rec.Issue.CreatedBy != tc.actor {
						t.Fatalf("journal = %+v, want actor=%q source=%q", rec, tc.actor, tc.source)
					}
				}
			}
			if !found {
				t.Fatalf("missing create for %s", id)
			}
		})
	}
}
