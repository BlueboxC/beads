//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/knowledge"
)

// This process test covers real Dolt persistence and prime/hook wiring. The
// pure source/hash semantics are covered in internal/knowledge, not repeated.
func TestKnowledgeProcessPersistenceAndHookProjection(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to exercise the real embedded store")
	}
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix=kn", "--stealth", "--skip-hooks", "--skip-agents", "--role=maintainer")
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, path)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("DOX.md", "# Project contract\n")
	write("docs/plan.md", "# Build the approved module\n")
	nested := filepath.Join(dir, "src/module")
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	run := func(cwd, input string, args ...string) string {
		t.Helper()
		cmd := exec.Command(bd, args...)
		cmd.Dir = cwd
		cmd.Env = bdEnv(dir)
		cmd.Stdin = strings.NewReader(input)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd %v: %v\nstdout=%s\nstderr=%s", args, err, stdout.String(), stderr.String())
		}
		return stdout.String()
	}
	run(dir, "", "remember", "Keep ordinary memories", "--key=plain")
	// Namespace flags keep readonly diagnostics bounded, including empty results.
	if got := run(nested, "", "--readonly", "memories", "--key-prefix=pla", "--json"); !strings.Contains(got, "Keep ordinary memories") {
		t.Fatal("literal prefix not wired")
	}
	var empty map[string]json.RawMessage
	if err := json.Unmarshal([]byte(run(nested, "", "--readonly", "memories", "--key-prefix=pla", "--exclude-key-prefix=plain", "--json")), &empty); err != nil {
		t.Fatal(err)
	}
	delete(empty, "schema_version")
	if len(empty) != 0 {
		t.Fatalf("literal exclusion returned memories: %v", empty)
	}
	run(nested, "", "knowledge", "scan", "DOX.md", "docs", "--json")
	assertion := `{"id":"approved-goal","kind":"objective","summary":"Continue the approved module without repeating finished work","module":"module","scope":"recorded","sources":[{"path":"docs/plan.md"}]}`
	run(nested, assertion, "knowledge", "record", "--file=-", "--json")
	var state knowledge.State
	if err := json.Unmarshal([]byte(run(nested, "", "knowledge", "list", "--json")), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Records) != 1 || state.Records[0].Validity != "current" || len(state.Catalog.Sources) != 2 {
		t.Fatalf("fresh process lost knowledge: %+v", state)
	}
	prime := run(nested, "", "prime", "--memories-only")
	if !strings.Contains(prime, "approved-goal") || !strings.Contains(prime, "Keep ordinary memories") || strings.Contains(prime, "knowledge_version") {
		t.Fatalf("projection leaked JSON or lost a plane: %s", prime)
	}
	if suppressed := run(nested, "", "prime", "--no-memories"); strings.Contains(suppressed, "approved-goal") {
		t.Fatal("--no-memories ignored")
	}
	write(".beads/PRIME.md", "# Custom workflow\n")
	if override := run(nested, "", "prime"); !strings.Contains(override, "Custom workflow") || !strings.Contains(override, "approved-goal") {
		t.Fatal("custom PRIME dropped source-backed knowledge")
	}
	hookInput, _ := json.Marshal(map[string]string{"session_id": "knowledge-smoke", "cwd": nested})
	for _, event := range []string{"SessionStart", "PreCompact", "PostCompact", "UserPromptSubmit"} {
		out := run(nested, string(hookInput), "codex-hook", event)
		if event == "SessionStart" || event == "UserPromptSubmit" {
			var response codexHookResponse
			if err := json.Unmarshal([]byte(out), &response); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(response.HookSpecificOutput.AdditionalContext, "approved-goal") {
				t.Fatalf("%s lost knowledge", event)
			}
		}
	}
	if out := run(nested, string(hookInput), "codex-hook", "UserPromptSubmit"); out != "" {
		t.Fatal("post-compaction injection repeated")
	}
	// A resumed session can project context before the next user prompt.
	// Exercise separate processes so the pending marker must be consumed on disk.
	run(nested, string(hookInput), "codex-hook", "PostCompact")
	if out := run(nested, string(hookInput), "codex-hook", "SessionStart"); !strings.Contains(out, "approved-goal") {
		t.Fatal("resumed session lost stored objective")
	}
	if out := run(nested, string(hookInput), "codex-hook", "UserPromptSubmit"); out != "" {
		t.Fatal("resumed session injected stored objective twice")
	}
	write("docs/plan.md", "# Changed plan\n")
	write("docs/new.md", "# New source\n")
	if context := run(nested, "", "knowledge", "context"); !strings.Contains(context, "needs_review") || !strings.Contains(context, "New document") {
		t.Fatalf("source changes lost: %s", context)
	}
	run(nested, "", "knowledge", "scan", "--json")
	if err := json.Unmarshal([]byte(run(nested, "", "knowledge", "list", "--json")), &state); err != nil {
		t.Fatal(err)
	}
	if state.Records[0].Validity != "needs_review" || state.Records[0].Scope != "recorded" {
		t.Fatal("catalog refresh silently revalidated or upgraded evidence")
	}
	var graph knowledge.Graph
	if err := json.Unmarshal([]byte(run(nested, "", "knowledge", "graph", "--json")), &graph); err != nil {
		t.Fatal(err)
	}
	if len(graph.Nodes) != 4 || len(graph.Edges) < 2 {
		t.Fatalf("stored graph: %+v", graph)
	}
	outside := t.TempDir()
	if out := run(outside, string(hookInput), "codex-hook", "SessionStart"); strings.Contains(out, "approved-goal") {
		t.Fatal("knowledge leaked outside initialized workspace")
	}
	// Distinct persistence regression: the old single TEXT value fails here.
	preserved := run(dir, "", "recall", "@knowledge/record/approved-goal", "--json")
	for i := 0; i < 150; i++ {
		content := ""
		for j := 0; j < 12; j++ {
			content += fmt.Sprintf("## %03d-%02d %s\n", i, j, strings.Repeat("x", 160))
		}
		write(fmt.Sprintf("docs/large-%03d.md", i), content)
	}
	run(nested, "", "knowledge", "scan", "--json")
	if err := json.Unmarshal([]byte(run(nested, "", "--readonly", "knowledge", "list", "--json")), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Catalog.Sources) != 153 || len(state.Warnings) > 0 {
		t.Fatalf("large catalog lost across processes: documents=%d warnings=%v", len(state.Catalog.Sources), state.Warnings)
	}
	if run(dir, "", "recall", "@knowledge/record/approved-goal", "--json") != preserved {
		t.Fatal("large catalog rebound assertion evidence")
	}
	if prime := run(nested, "", "prime", "--memories-only"); strings.Contains(prime, "catalog_parts") || len(prime) > 9000 {
		t.Fatal("catalog storage leaked or expanded context")
	}

}
