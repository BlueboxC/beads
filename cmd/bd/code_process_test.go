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
	"github.com/steveyegge/beads/internal/knowledge"
	"github.com/steveyegge/beads/internal/types"
)

// Distinct process seam: real Dolt publication, nested workspace selection,
// symbol-linked assertions and bounded prime projection must agree after restart.
func TestCodeIndexProcessPersistenceAndSymbolLearning(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 for the real embedded store")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("operator Python 3 unavailable")
	}
	bd := buildEmbeddedBD(t)
	licenseCommand := exec.Command(bd, "code", "--licenses")
	licenseCommand.Dir = t.TempDir()
	licenseCommand.Env = bdEnv(licenseCommand.Dir)
	if output, err := licenseCommand.Output(); err != nil || !strings.Contains(string(output), "Apache License") {
		t.Fatalf("store-free licenses: %v", err)
	}
	dir, _, _ := bdInit(t, bd, "--prefix=ci", "--stealth", "--skip-hooks", "--skip-agents", "--role=maintainer")
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, path)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("DOX.md", "# Root contract\n")
	write("src/fix.py", "def existing_solution(): return 42\n")
	nested := filepath.Join(dir, "src")
	run := func(input string, args ...string) string {
		t.Helper()
		cmd := exec.Command(bd, args...)
		cmd.Dir = nested
		cmd.Env = bdEnv(dir)
		cmd.Stdin = strings.NewReader(input)
		out, errout, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd %v: %v\n%s\n%s", args, err, out.String(), errout.String())
		}
		return out.String()
	}
	run("", "remember", "Preserve original direction", "--key=human")
	run("", "code", "scan", "src", "--json")
	assertion := `{"id":"learned-fix","kind":"solution","summary":"Reuse existing solution","module":"src","issue":"ci-ref","scope":"inspected","evidence":"Implementation inspected; no project test execution","symbols":["src/fix.py::existing_solution"],"sources":[{"path":"DOX.md"}]}`
	run(assertion, "knowledge", "record", "--file=-", "--json")
	var query codeindex.Query
	if err := json.Unmarshal([]byte(run("", "code", "query", "ci-ref", "--json")), &query); err != nil {
		t.Fatal(err)
	}
	if len(query.Files) != 1 || len(query.Knowledge) != 1 || query.Knowledge[0].Validity != "current" {
		t.Fatalf("restart lost symbol-linked knowledge: %+v", query)
	}
	before := run("", "knowledge", "list", "--json")
	run("", "code", "scan", "--rebuild", "--json")
	if after := run("", "knowledge", "list", "--json"); before != after {
		t.Fatal("rebuild changed explicit learning")
	}
	prime := run("", "prime", "--memories-only")
	if !strings.Contains(prime, "1 files, 1 symbols") || !strings.Contains(prime, "learned-fix") || !strings.Contains(prime, "Preserve original direction") || strings.Contains(prime, "code-v1:") || strings.Contains(prime, "code_index_version") {
		t.Fatalf("prime lost scope or injected AST blobs: %s", prime)
	}

	var task types.Issue
	if err := json.Unmarshal([]byte(run("", "create", "Already solved", "--json")), &task); err != nil {
		t.Fatal(err)
	}
	run("", "close", task.ID)
	var page graphview.Page
	if err := json.Unmarshal([]byte(run("", "graph", "--project", "--readonly", "--json")), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Files) != 1 || len(page.Files[0].Symbols) != 1 || page.Files[0].Symbols[0].Line != 1 {
		t.Fatal("CLI overview lost persisted source locations")
	}
	closed := false
	for _, node := range page.Nodes {
		if node.ID == "issue:"+task.ID && node.Status == "closed" {
			closed = true
		}
	}
	if !closed {
		t.Fatal("CLI overview omitted a completed task")
	}
	if after := run("", "knowledge", "list", "--json"); before != after {
		t.Fatal("overview wrote or renewed human knowledge")
	}
	if !strings.Contains(run("", "graph", "--project", "--readonly", "--html"), "src/fix.py") {
		t.Fatal("overview HTML missing source evidence")
	}
	if _, err := exec.LookPath("node"); err == nil {
		write("go.mod", "module example.local/fixture\n\ngo 1.26\n")
		write("native/a.go", "package native\nfunc Existing() int {return 42}\n")
		write("native/b.ts", "export function learned(): number {return 42}\n")
		write("native/c.js", "import {learned} from './b';\nexport function use(){return learned()}\nthrow Error('never execute');\n")
		run("", "code", "scan", "src", "native", "--languages=python,go,javascript,typescript", "--json")
		if after := run("", "knowledge", "list", "--json"); before != after {
			t.Fatal("multi-language scan changed Python learning")
		}
		native := `{"id":"native-fix","kind":"solution","summary":"Existing native implementation","module":"native","scope":"inspected","evidence":"source inspection only","symbols":["native/a.go::Existing","native/b.ts::learned"],"sources":[{"path":"DOX.md"}]}`
		run(native, "knowledge", "record", "--file=-", "--json")
		if err := json.Unmarshal([]byte(run("", "code", "query", "native-fix", "--json")), &query); err != nil {
			t.Fatal(err)
		}
		if len(query.Files) != 2 {
			t.Fatalf("native symbol links lost: %+v", query)
		}
		run("", "code", "scan", "--json")
		before = run("", "knowledge", "list", "--json")
	}
	write("src/fix.py", "def replacement(): return 0\n")
	run("", "code", "scan", "--json")
	var state knowledge.State
	if err := json.Unmarshal([]byte(run("", "knowledge", "list", "--json")), &state); err != nil {
		t.Fatal(err)
	}
	var original *knowledge.View
	for i := range state.Records {
		if state.Records[i].ID == "learned-fix" {
			original = &state.Records[i]
		}
	}
	if original == nil || original.Validity != "needs_review" || original.Scope != "inspected" {
		t.Fatal("scan renewed changed learning")
	}
	cmd := exec.Command(bd, "knowledge", "record", "--file=-", "--json")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	cmd.Stdin = strings.NewReader(assertion)
	if err := cmd.Run(); err == nil {
		t.Fatal("missing named symbol accepted")
	}
}
