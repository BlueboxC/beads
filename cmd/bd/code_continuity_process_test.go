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
)

// Real process seam: atomic index publication/pruning and readonly relink drafts
// must preserve established learnings and closed work across independent clients.
func TestCodeContinuityProcessPreservesHumanKnowledgeAndIndex(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1")
	}
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix=cc", "--stealth", "--skip-hooks", "--skip-agents", "--role=maintainer")
	write := func(p, s string) {
		t.Helper()
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(s), 0600); err != nil {
			t.Fatal(err)
		}
	}
	run := func(input string, args ...string) string {
		t.Helper()
		cmd := exec.Command(bd, args...)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		cmd.Stdin = strings.NewReader(input)
		out, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd %v: %v %s", args, err, stderr.String())
		}
		return out.String()
	}
	write("DOX.md", "# Root contract\n")
	write("src/fix.py", "def solved(): return 42\n")
	write("tests/test_fix.py", "from src.fix import solved\ndef test_fix(): return solved()\n")
	run("", "remember", "Keep objective", "--key=human")
	task := run("", "create", "Completed fix", "--json")
	var issue struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(task), &issue); err != nil {
		t.Fatal(err)
	}
	run("", "close", issue.ID)
	run("", "code", "scan", "src", "tests", "--json")
	record := `{"id":"fix","kind":"solution","summary":"Existing fix","scope":"inspected","evidence":"Historical source inspection","symbols":["src/fix.py::solved"],"sources":[{"path":"src/fix.py"},{"path":"tests/test_fix.py"}]}`
	run(record, "knowledge", "record", "--file=-", "--json")
	before := run("", "recall", "@knowledge/record/fix", "--json")
	if err := os.Rename(filepath.Join(dir, "src/fix.py"), filepath.Join(dir, "src/moved.py")); err != nil {
		t.Fatal(err)
	}
	run("", "code", "scan", "--json")
	var relink codeindex.RelinkReport
	if err := json.Unmarshal([]byte(run("", "--readonly", "code", "relink", "src/fix.py", "src/moved.py", "--json")), &relink); err != nil || len(relink.Drafts) != 1 || relink.Drafts[0].Draft.Scope != "recorded" {
		t.Fatalf("relink: %+v %v", relink, err)
	}
	write("tests/test_fix.py", "from src.moved import solved\ndef test_fix(): return solved()\n")
	run("", "code", "scan", "--json")
	var impact codeindex.ImpactReport
	if err := json.Unmarshal([]byte(run("", "--readonly", "code", "impact", "src/moved.py", "--json")), &impact); err != nil || len(impact.Files) != 2 || len(impact.Tests) != 1 {
		t.Fatalf("impact: %+v %v", impact, err)
	}
	var plan codeindex.PrunePlan
	if err := json.Unmarshal([]byte(run("", "--readonly", "code", "prune", "--json")), &plan); err != nil || len(plan.ObsoleteKeys) == 0 {
		t.Fatalf("prune plan: %+v %v", plan, err)
	}
	refused := exec.Command(bd, "--readonly", "code", "prune", "--apply", "--json")
	refused.Dir = dir
	refused.Env = bdEnv(dir)
	if err := refused.Run(); err == nil {
		t.Fatal("readonly prune applied")
	}
	if err := json.Unmarshal([]byte(run("", "code", "prune", "--apply", "--json")), &plan); err != nil || plan.Deleted == 0 || !plan.HistoryRetained {
		t.Fatalf("prune apply: %+v %v", plan, err)
	}
	if run("", "recall", "@knowledge/record/fix", "--json") != before || !strings.Contains(run("", "recall", "human"), "Keep objective") || !strings.Contains(run("", "show", issue.ID, "--json"), `"status": "closed"`) {
		t.Fatal("derived cleanup changed established work")
	}
	run("", "--readonly", "code", "status", "--json")
	run("", "code", "scan", "--rebuild", "--json")
	if run("", "recall", "@knowledge/record/fix", "--json") != before {
		t.Fatal("rebuild renewed evidence")
	}
	if err := json.Unmarshal([]byte(run("", "code", "prune", "--apply", "--json")), &plan); err != nil {
		t.Fatal(err)
	}
	run("", "--readonly", "code", "query", "src/moved.py", "--json")
}
