//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/codeindex"
	"github.com/steveyegge/beads/internal/graphview"
)

// Real separate CLI processes verify saved selections, symbol-backed knowledge
// and graph projection against the embedded storage boundary.
func TestEightLanguageCodeProcessPersistence(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 for the real embedded store")
	}
	for _, name := range []string{"node", "python3"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skip("operator interpreter unavailable: " + name)
		}
	}
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix=cl", "--stealth", "--skip-hooks", "--skip-agents", "--role=maintainer")
	sources := map[string]string{"keep.py": "def keep(): return 1\n", "main.go": "package main\nfunc Keep(){}", "main.js": "function keep(){}", "main.ts": "function keep():void{}", "Main.java": "class Main {static void keep(){}}", "Main.cs": "class Main {static void Keep(){}}", "main.rs": "fn keep(){}", "main.cpp": "void keep(){}"}
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, code := range sources {
		if err := os.WriteFile(filepath.Join(dir, "src", name), []byte(code), 0600); err != nil {
			t.Fatal(err)
		}
	}
	run := func(input string, args ...string) string {
		t.Helper()
		cmd := exec.Command(bd, args...)
		cmd.Dir = filepath.Join(dir, "src")
		cmd.Env = bdEnv(dir)
		cmd.Stdin = strings.NewReader(input)
		out, errout, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd %v: %v\n%s\n%s", args, err, out.String(), errout.String())
		}
		return out.String()
	}
	run("", "remember", "preserve original evidence", "--key=human")
	var scan struct {
		Stats     codeindex.Stats   `json:"stats"`
		Changes   codeindex.Changes `json:"changes"`
		Languages []string          `json:"languages"`
	}
	if err := json.Unmarshal([]byte(run("", "code", "scan", "src", "--languages=all", "--json")), &scan); err != nil {
		t.Fatal(err)
	}
	if scan.Stats.Files != 8 || scan.Stats.ParseErrors != 0 || len(scan.Languages) != 8 {
		t.Fatalf("scan %+v", scan)
	}
	run(`{"id":"tree-solution","kind":"solution","summary":"Keep existing Java implementation","scope":"inspected","evidence":"Source inspection only; no Java execution","symbols":["src/Main.java::Main.keep","src/Main.cs::Main.Keep","src/main.rs::keep","src/main.cpp::keep"],"sources":[{"path":"src/Main.java"}]}`, "knowledge", "record", "--file=-", "--json")
	before := run("", "knowledge", "list", "--json")
	if err := json.Unmarshal([]byte(run("", "code", "scan", "--node=missing-node", "--python=missing-python", "--json")), &scan); err != nil {
		t.Fatal(err)
	}
	if scan.Changes.Parsed != 0 || scan.Changes.Reused != 8 || len(scan.Languages) != 8 {
		t.Fatalf("saved selection/reuse %+v", scan)
	}
	run("", "code", "scan", "--rebuild", "--json")
	if before != run("", "knowledge", "list", "--json") {
		t.Fatal("rebuild renewed human evidence")
	}
	var query codeindex.Query
	if err := json.Unmarshal([]byte(run("", "code", "query", "src/Main.java", "--json")), &query); err != nil {
		t.Fatal(err)
	}
	if len(query.Files) != 1 || len(query.Knowledge) != 1 || query.Knowledge[0].Validity != "current" {
		t.Fatalf("symbol-backed learning %+v", query)
	}
	var page graphview.Page
	if err := json.Unmarshal([]byte(run("", "graph", "--project", "--readonly", "--json")), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Files) != 8 {
		t.Fatalf("graph files=%d", len(page.Files))
	}
	for _, file := range page.Files {
		if len(file.Symbols) == 0 {
			t.Fatalf("graph lacks symbols for %+v", file)
		}
	}
	if !strings.Contains(run("", "memories", "--json"), "preserve original evidence") {
		t.Fatal("human memory lost")
	}
}
