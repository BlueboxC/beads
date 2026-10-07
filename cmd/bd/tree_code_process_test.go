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
	if err := json.Unmarshal([]byte(run("", "code", "scan", "src", "--languages=python,go,javascript,typescript,java,csharp,rust,cpp", "--json")), &scan); err != nil {
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
	// Expand the same store: old evidence remains current and every new format
	// can link a reviewed symbol without a second storage boundary.
	additions := map[string]string{
		"main.php":     "<?php namespace Demo; use App\\Service; class Box { public function run($x) { return helper($x); } } function helper($x) { return $x; } function go() { helper(1); } include 'lib.php';",
		"main.c":       "#include \"lib.h\"\nstruct Box { int x; }; typedef struct Box Box; int helper(int x) {return x;} int run(void) {return helper(1);}",
		"main.sh":      "#!/bin/sh\nsource ./lib.sh\nhelper() { echo ok; }\nrun() { helper; }\nrun\n",
		"main.ps1":     ". ./lib.ps1\nfunction Helper {\nparam($x)\nWrite-Output $x\n}\nfunction Run { Helper 1 }\nRun\n",
		"main.html":    "<!doctype html><html><head><link rel=\"stylesheet\" href=\"./main.css\"></head><body><main id=\"app\" class=\"panel\"><button>Go</button><script src=\"./main.js\"></script></main></body></html>",
		"main.css":     "@import './base.css'; .panel, #app { color: red; display: grid; } @media screen { button:hover { color: blue; } }",
		"main.graphql": "type User { id: ID! name: String } type Query { user: User } query GetUser { user { id } } fragment UserFields on User { id name } query More { user { ...UserFields } }",
		"main.xml":     "<?xml version=\"1.0\"?><layout xmlns:android=\"urn:android\"><item name=\"title\">PRIVATE_LITERAL</item><item/></layout>",
		"Main.kt":      "package demo\nimport app.Helper\nclass Box {\nfun run(x: Int): Int {\nreturn helper(x)\n}\n}\nfun helper(x: Int): Int {\nreturn x\n}\nfun go() {\nhelper(1)\n}\n",
		"Main.swift":   "import Foundation\nstruct Box { func run(_ x: Int) -> Int { return helper(x) } }\nfunc helper(_ x: Int) -> Int { return x }\nfunc go() { helper(1) }\n",
		"main.dart":    "import 'lib.dart'; class Box { int run(int x) { return helper(x); } } int helper(int x) {return x;} void go() { helper(1); }",
		"main.sql":     "CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT); CREATE VIEW active AS SELECT id FROM users; SELECT * FROM active;",
		"main.json":    "{\"service\": {\"token\": \"PRIVATE_LITERAL\", \"enabled\": true}, \"items\": [{\"id\":1}]}",
		"main.yaml":    "service:\n  token: PRIVATE_LITERAL\n  enabled: true\nitems:\n  - id: 1\n",
		"main.toml":    "[service]\ntoken = 'PRIVATE_LITERAL'\nenabled = true\n[[items]]\nid = 1\n",
	}

	for name, code := range additions {
		if err := os.WriteFile(filepath.Join(dir, "src", name), []byte(code), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := json.Unmarshal([]byte(run("", "code", "scan", "--languages=all", "--json")), &scan); err != nil {
		t.Fatal(err)
	}
	if scan.Stats.Files != 23 || scan.Stats.ParseErrors != 0 || len(scan.Languages) != 23 || scan.Changes.Reused != 8 {
		t.Fatalf("23-language expansion %+v", scan)
	}
	if before != run("", "knowledge", "list", "--json") {
		t.Fatal("expansion renewed original learning")
	}
	var complete codeindex.Query
	if err := json.Unmarshal([]byte(run("", "code", "query", "--limit=50", "--json")), &complete); err != nil {
		t.Fatal(err)
	}
	symbols := []string{}
	for _, file := range complete.Files {
		if _, added := additions[filepath.Base(file.Path)]; added {
			if len(file.Symbols) == 0 {
				t.Fatal("no symbols for " + file.Path)
			}
			symbols = append(symbols, file.Symbols[0].ID)
		}
	}
	if len(symbols) != 15 {
		t.Fatalf("new-format symbols=%d", len(symbols))
	}
	record := map[string]any{"id": "twenty-three-solution", "kind": "solution", "summary": "Retain new format structure", "scope": "inspected", "evidence": "Fixture source inspection only; no application execution", "symbols": symbols, "sources": []map[string]string{{"path": "src/main.php"}}}
	data, _ := json.Marshal(record)
	run(string(data), "knowledge", "record", "--file=-", "--json")
	newBefore := run("", "knowledge", "list", "--json")
	if err := json.Unmarshal([]byte(run("", "code", "scan", "--node=missing-node", "--python=missing-python", "--json")), &scan); err != nil {
		t.Fatal(err)
	}
	if scan.Changes.Reused != 23 || scan.Changes.Parsed != 0 {
		t.Fatalf("new-format no-op %+v", scan)
	}
	run("", "code", "scan", "--rebuild", "--json")
	if newBefore != run("", "knowledge", "list", "--json") {
		t.Fatal("new-format rebuild renewed learning")
	}
	if err := json.Unmarshal([]byte(run("", "graph", "--project", "--readonly", "--json")), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Files) != 23 {
		t.Fatalf("expanded graph files=%d", len(page.Files))
	}
	for _, file := range page.Files {
		if len(file.Symbols) == 0 {
			t.Fatal("expanded graph missing " + file.Path)
		}
	}

}
