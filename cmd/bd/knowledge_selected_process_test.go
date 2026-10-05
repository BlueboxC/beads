//go:build cgo

package main

import (
	"encoding/json"
	"github.com/steveyegge/beads/internal/knowledge"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelectedSymbolProposalProcessPreservesPendingAndRejectsStale(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 for real Dolt")
	}
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix=sp", "--stealth", "--skip-hooks", "--skip-agents", "--role=maintainer")
	write := func(path, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, path), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("DOX.md", "# Contract\n")
	write("fix.go", "package fixture\nfunc Solved() {}\n")
	write("other.go", "package fixture\nfunc Other() {}\n")
	run := func(input string, args ...string) (string, error) {
		t.Helper()
		cmd := exec.Command(bd, args...)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		cmd.Stdin = strings.NewReader(input)
		out, errout, err := runCommandBuffers(t, cmd)
		return out.String() + errout.String(), err
	}
	must := func(input string, args ...string) string {
		t.Helper()
		out, err := run(input, args...)
		if err != nil {
			t.Fatalf("%v: %v %s", args, err, out)
		}
		return out
	}
	must("", "code", "scan", "fix.go", "other.go", "--languages=go", "--json")
	write("other.go", "unrelated stale source\n")
	var sources []knowledge.Source
	if err := json.Unmarshal([]byte(must("", "--readonly", "knowledge", "sources", "fix.go", "--json")), &sources); err != nil {
		t.Fatal(err)
	}
	record := knowledge.Record{ID: "linked-fix", Kind: "solution", Summary: "Keep a linked solution pending", Scope: "recorded", Sources: sources, Symbols: []string{"fix.go::Solved"}}
	data, _ := json.Marshal(record)
	var proposal knowledge.Proposal
	if err := json.Unmarshal([]byte(must(string(data), "knowledge", "propose", "--file=-", "--json")), &proposal); err != nil {
		t.Fatal(err)
	}
	var pending []knowledge.ProposalView
	if err := json.Unmarshal([]byte(must("", "--readonly", "knowledge", "proposals", "--json")), &pending); err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != proposal.ID || pending[0].Status != "pending" {
		t.Fatal("proposal promoted")
	}
	before := must("", "--readonly", "memories", "--exclude-key-prefix=@knowledge/code/", "--json")
	must(string(data), "knowledge", "propose", "--file=-", "--json")
	if got := must("", "--readonly", "memories", "--exclude-key-prefix=@knowledge/code/", "--json"); got != before {
		t.Fatal("retry changed human values")
	}
	write("fix.go", "package fixture\nfunc Changed() {}\n")
	if err := json.Unmarshal([]byte(must("", "--readonly", "knowledge", "sources", "fix.go", "--json")), &sources); err != nil {
		t.Fatal(err)
	}
	record.ID = "stale-symbol"
	record.Sources = sources
	data, _ = json.Marshal(record)
	if out, err := run(string(data), "knowledge", "propose", "--file=-", "--json"); err == nil || !strings.Contains(out, "missing or stale") {
		t.Fatalf("stale indexed symbol allowed: %s %v", out, err)
	}
	if got := must("", "--readonly", "memories", "--exclude-key-prefix=@knowledge/code/", "--json"); got != before {
		t.Fatal("failed validation wrote a proposal")
	}
}
