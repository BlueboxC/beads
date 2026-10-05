//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/knowledge"
)

// Covers process wiring, persistent single-entry review/assertion, nested-root
// selection, readonly mutation guards and project isolation with real Dolt.
func TestKnowledgeProposalsProcessPersistenceReadonlyAndIsolation(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 for real embedded persistence")
	}
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix=kp", "--stealth", "--skip-hooks", "--skip-agents", "--role=maintainer")
	other, _, _ := bdInit(t, bd, "--prefix=other", "--stealth", "--skip-hooks", "--skip-agents", "--role=maintainer")
	if err := os.WriteFile(filepath.Join(dir, "DOX.md"), []byte("# Contract\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "solution.md"), []byte("# Solution\nPreserve reviewed evidence\n"), 0600); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(dir, "module")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	run := func(cwd, input string, args ...string) (string, error) {
		t.Helper()
		cmd := exec.Command(bd, args...)
		cmd.Dir = cwd
		cmd.Env = bdEnv(cwd)
		cmd.Stdin = strings.NewReader(input)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			return stdout.String() + stderr.String(), err
		}
		return stdout.String(), nil
	}
	must := func(cwd, input string, args ...string) string {
		t.Helper()
		out, err := run(cwd, input, args...)
		if err != nil {
			t.Fatalf("%v: %v %s", args, err, out)
		}
		return out
	}
	var sources []knowledge.Source
	if err := json.Unmarshal([]byte(must(nested, "", "knowledge", "sources", "solution.md", "--json")), &sources); err != nil {
		t.Fatal(err)
	}
	record := knowledge.Record{ID: "preserved-fix", Kind: "solution", Summary: "Do not repeat a solved repair", Problem: "Lost context", Solution: "Persist reviewed evidence", Scope: "recorded", Sources: sources}
	data, _ := json.Marshal(record)
	var p knowledge.Proposal
	if err := json.Unmarshal([]byte(must(nested, string(data), "knowledge", "propose", "--file=-", "--json")), &p); err != nil {
		t.Fatal(err)
	}
	before := must(dir, "", "memories", "--json")
	must(nested, string(data), "knowledge", "propose", "--file=-", "--json")
	if before != must(dir, "", "memories", "--json") {
		t.Fatal("proposal retry mutated stored value")
	}
	if out, err := run(nested, string(data), "--readonly", "knowledge", "propose", "--file=-"); err == nil || !strings.Contains(out, "read-only") {
		t.Fatalf("readonly proposal allowed: %s", out)
	}
	if out, err := run(nested, "", "--readonly", "knowledge", "review", p.ID, "--decision=reject", "--reviewer=test", "--reason=test"); err == nil || !strings.Contains(out, "read-only") {
		t.Fatalf("readonly review allowed: %s", out)
	}
	if prime := must(nested, "", "prime", "--memories-only"); !strings.Contains(prime, "pending=1") || strings.Contains(prime, "Persist reviewed evidence") {
		t.Fatal("pending proposal became recovered solution")
	}
	if isolated := must(other, "", "knowledge", "proposals", "--json"); strings.Contains(isolated, p.ID) {
		t.Fatal("proposal leaked across projects")
	}
	args := []string{"knowledge", "review", p.ID, "--decision=accept", "--reviewer=process supervisor", "--reason=Source inspected", "--scope=inspected", "--evidence=Historical definitions read; no execution", "--json"}
	must(nested, "", args...)
	accepted := must(dir, "", "memories", "--json")
	must(nested, "", args...)
	if accepted != must(dir, "", "memories", "--json") {
		t.Fatal("review retry mutated stored value")
	}
	var state knowledge.State
	if err := json.Unmarshal([]byte(must(dir, "", "knowledge", "list", "--json")), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Records) != 1 || len(state.Proposals) != 1 || state.Proposals[0].Status != "accepted" {
		t.Fatalf("fresh process lost promotion: %+v", state)
	}
	hookInput, _ := json.Marshal(map[string]string{"session_id": "proposal-process", "cwd": nested})
	if hook := must(nested, string(hookInput), "codex-hook", "SessionStart"); !strings.Contains(hook, "preserved-fix") || strings.Contains(hook, "proposal_version") {
		t.Fatal("hook lost accepted learning or leaked raw ledger")
	}
	if noMem := must(nested, "", "prime", "--no-memories"); strings.Contains(noMem, "Supervised proposal ledger") {
		t.Fatal("no-memories ignored proposals")
	}
}
