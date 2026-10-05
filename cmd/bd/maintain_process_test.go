//go:build cgo

package main

import (
	"bufio"
	"encoding/json"
	"github.com/steveyegge/beads/internal/knowledge"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Real lifecycle seam: watcher releases the embedded lock, observes source
// changes after process restart, and neither runs hooks nor writes a foreign DB.
func TestMaintenanceProcessWatchIsolationAndReadonlyBoundaries(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 for actual embedded lifecycle")
	}
	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix=maint", "--stealth", "--skip-hooks", "--skip-agents", "--role=maintainer")
	foreign, _, _ := bdInit(t, bd, "--prefix=other", "--stealth", "--skip-hooks", "--skip-agents", "--role=maintainer")
	write := func(path, text string) {
		t.Helper()
		p := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	env := graphViewerEnv(bdEnv(dir), beadsDir, "maint")
	absent := exec.Command(bd, "maintain", "once")
	absent.Dir, absent.Env = dir, graphViewerEnv(bdEnv(dir), beadsDir, "not_initialized")
	if err := absent.Run(); err == nil {
		t.Fatal("maintenance initialized an absent database")
	}
	if _, err := os.Stat(filepath.Join(beadsDir, "embeddeddolt", "not_initialized")); !os.IsNotExist(err) {
		t.Fatal("absent database was provisioned")
	}
	run := func(input string, args ...string) string {
		t.Helper()
		cmd := exec.Command(bd, args...)
		cmd.Dir = dir
		cmd.Env = env
		cmd.Stdin = strings.NewReader(input)
		out, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("%v: %v %s %s", args, err, out.String(), stderr.String())
		}
		return out.String()
	}
	write("DOX.md", "# Contract\n")
	write("src/a.go", "package src\nfunc AlreadySolved(){}\n")
	write("docs/plan.md", "# Plan\n")
	run("", "code", "scan", "src", "--languages=go")
	run("", "knowledge", "scan", "docs")
	run(`{"id":"fix","kind":"solution","summary":"Already solved","scope":"inspected","evidence":"source inspected; no execution","sources":[{"path":"src/a.go"}]}`, "knowledge", "record", "--file=-")
	var rows map[string]json.RawMessage
	if err := json.Unmarshal([]byte(run("", "memories", "--readonly", "--json")), &rows); err != nil {
		t.Fatal(err)
	}
	original := string(rows["@knowledge/record/fix"])
	write(".beads/hooks/on_update", "#!/bin/sh\ntouch '"+filepath.Join(dir, "hook-ran")+"'\n")
	if err := os.Chmod(filepath.Join(beadsDir, "hooks/on_update"), 0700); err != nil {
		t.Fatal(err)
	}
	watcher := exec.Command(bd, "maintain", "watch", "--interval=1s", "--json")
	watcher.Dir = dir
	watcher.Env = env
	stdout, err := watcher.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	watcher.Stderr = &stderr
	if err := watcher.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = watcher.Process.Kill(); _ = watcher.Wait() })
	results := make(chan maintenanceResult, 16)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			var r maintenanceResult
			if json.Unmarshal(scanner.Bytes(), &r) == nil {
				results <- r
			}
		}
	}()
	next := func() maintenanceResult {
		t.Helper()
		select {
		case r := <-results:
			return r
		case <-time.After(20 * time.Second):
			t.Fatal("watcher emitted no bounded pass")
			return maintenanceResult{}
		}
	}
	if r := next(); r.CodeUpdated || r.CatalogUpdated {
		t.Fatal("initial unchanged pass wrote")
	}
	// Try a CLI writer while watch is waiting: a persistent store would block it.
	run("", "remember", "Conserve original direction", "--key=human")
	write(".beads/.env", "BEADS_DIR="+filepath.Join(foreign, ".beads")+"\nBEADS_DOLT_SERVER_DATABASE=other\n")
	write("src/a.go", "package src\nfunc Repaired(){}\n")
	write("src/new.go", "package src\nfunc Added(){}\n")
	write("docs/plan.md", "# Changed plan\n")
	observed := false
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		r := next()
		if r.CodeUpdated && r.CatalogUpdated {
			observed = true
			break
		}
	}
	if !observed {
		t.Fatal("watcher did not publish the changed selection")
	}
	var state knowledge.State
	if err := json.Unmarshal([]byte(run("", "knowledge", "list", "--readonly", "--json")), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Records) != 1 || state.Records[0].Validity != "needs_review" {
		t.Fatal("watcher renewed solved evidence")
	}
	if err := json.Unmarshal([]byte(run("", "memories", "--readonly", "--json")), &rows); err != nil {
		t.Fatal(err)
	}
	if string(rows["@knowledge/record/fix"]) != original || string(rows["human"]) != `"Conserve original direction"` {
		t.Fatal("human memory changed")
	}
	if _, err := os.Stat(filepath.Join(dir, "hook-ran")); !os.IsNotExist(err) {
		t.Fatal("project hook executed")
	}
	cmd := exec.Command(bd, "maintain", "once", "--readonly")
	cmd.Dir = dir
	cmd.Env = env
	if err := cmd.Run(); err == nil {
		t.Fatal("strict readonly allowed maintenance")
	}
	cmd = exec.Command(bd, "code", "status", "--readonly", "--json")
	cmd.Dir = foreign
	cmd.Env = bdEnv(foreign)
	if err := cmd.Run(); err == nil {
		t.Fatal("maintenance wrote a foreign index")
	}
	_ = watcher.Process.Signal(os.Interrupt)
	// Stop only the owned watcher; cleanup uses Wait to release it. Source code
	// remains inert throughout this Go-only process scenario.
}
