//go:build cgo && unix

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHumanGateCLI(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1")
	}
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "hgcli", "--skip-hooks", "--skip-agents")
	env := bdEnv(dir)
	for _, key := range []string{"BEADS_ACTOR", "BD_ACTOR", "BD_EVENTS_JOURNAL", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_NOSYSTEM"} {
		env = envWithout(env, key)
	}
	env = append(env, "BD_EVENTS_JOURNAL=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	run := func(ok bool, args ...string) string {
		t.Helper()
		cmd := exec.Command(bd, append([]string{"--sandbox"}, args...)...)
		cmd.Dir = dir
		cmd.Env = env
		var out, stderr bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &stderr
		err := cmd.Run()
		if ok && err != nil {
			t.Fatalf("bd %v: %v: %s", args, err, stderr.String())
		}
		if !ok && (err == nil || !strings.Contains(stderr.String()+out.String(), "human gate")) {
			t.Fatalf("bd %v: expected human-gate refusal, got %v: %s %s", args, err, out.String(), stderr.String())
		}
		return out.String()
	}
	for _, args := range [][]string{{"init", "-q"}, {"config", "core.hooksPath", filepath.Join(dir, "disabled-hooks")}, {"config", "user.name", "owner"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	task := strings.TrimSpace(run(true, "--actor", "agent", "create", "gated task", "--silent"))
	var gate struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(run(true, "--actor", "agent", "gate", "create", "--blocks", task, "--type", "human", "--json")), &gate); err != nil {
		t.Fatal(err)
	}
	if gate.ID == "" {
		t.Fatal("missing gate")
	}
	run(true, "config", "set", "gates.human.resolvers", `["owner"]`)
	before := run(true, "events", "export")
	for _, args := range [][]string{
		{"gate", "resolve", gate.ID}, // Git owner fallback must not count as explicit approval.
		{"--actor", "agent", "gate", "resolve", gate.ID},
		{"--actor", "agent", "close", gate.ID, "--force"},
		{"--actor", "agent", "close", task, "--force"},
		{"--actor", "agent", "update", gate.ID, "--status", "closed"},
		{"--actor", "agent", "update", gate.ID, "--type", "task"},
		{"--actor", "agent", "dep", "remove", task, gate.ID},
	} {
		run(false, args...)
	}
	if after := run(true, "events", "export"); before != after {
		t.Fatal("refusals changed journal")
	}
	var ready []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(run(true, "ready", "--json")), &ready); err != nil {
		t.Fatal(err)
	}
	for _, issue := range ready {
		if issue.ID == task {
			t.Fatal("task became ready before approval")
		}
	}
	run(true, "--actor", "owner", "gate", "resolve", gate.ID)
	if err := json.Unmarshal([]byte(run(true, "ready", "--json")), &ready); err != nil {
		t.Fatal(err)
	}
	for _, issue := range ready {
		if issue.ID == task {
			return
		}
	}
	t.Fatal("approved task missing from ready")
}
