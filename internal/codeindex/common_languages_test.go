package codeindex

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var commonSamples = map[string]struct{ path, code, wanted, bad string }{
	"php":        {"main.php", "<?php namespace Demo; use App\\Service; class Box { public function run($x) { return helper($x); } } function helper($x) { return $x; } function go() { helper(1); } include 'lib.php';\necho 'PRIVATE_LITERAL';", "Demo.Box.run", "<?php function bad( {"},
	"c":          {"main.c", "#include \"lib.h\"\nstruct Box { int x; }; typedef struct Box Box; int helper(int x) {return x;} int run(void) {return helper(1);}\nconst char *secret = \"PRIVATE_LITERAL\";", "run", "int bad( {"},
	"bash":       {"main.sh", "#!/bin/sh\nsource ./lib.sh\nhelper() { echo ok; }\nrun() { helper; }\nrun\n\nprintf '%s' 'PRIVATE_LITERAL'\n", "helper", "bad() {"},
	"powershell": {"main.ps1", ". ./lib.ps1\nfunction Helper {\nparam($x)\nWrite-Output $x\n}\nfunction Run { Helper 1 }\nRun\n\nWrite-Output 'PRIVATE_LITERAL'\n", "Helper", "function Bad {"},
	"html":       {"main.html", "<!doctype html><html><head><link rel=\"stylesheet\" href=\"./main.css\"></head><body><main id=\"app\" class=\"panel\"><button>Go</button><script src=\"./main.js\"></script></main></body></html><div data-token=\"PRIVATE_LITERAL\"><script>throw \"PRIVATE_LITERAL\";</script></div>", "html.body.main", "<div "},
	"css":        {"main.css", "@import './base.css'; .panel, #app { color: red; display: grid; } @media screen { button:hover { color: blue; } }.secret { content: \"PRIVATE_LITERAL\"; }", "rule@L1.display", ".bad { color:"},
	"graphql":    {"main.graphql", "type User { id: ID! name: String } type Query { user: User } query GetUser { user { id } } fragment UserFields on User { id name } query More { user { ...UserFields } }\n\"PRIVATE_LITERAL\" type Secret { id: ID }", "User.id", "type Bad {"},
	"xml":        {"main.xml", "<?xml version=\"1.0\"?><layout xmlns:android=\"urn:android\"><item name=\"title\">PRIVATE_LITERAL</item><item/></layout>", "layout.item", "<broken>"},
	"kotlin":     {"Main.kt", "package demo\nimport app.Helper\nclass Box {\nfun run(x: Int): Int {\nreturn helper(x)\n}\n}\nfun helper(x: Int): Int {\nreturn x\n}\nfun go() {\nhelper(1)\n}\n\nval secret = \"PRIVATE_LITERAL\"\n", "Box.run", "fun bad( {"},
	"swift":      {"Main.swift", "import Foundation\nstruct Box { func run(_ x: Int) -> Int { return helper(x) } }\nfunc helper(_ x: Int) -> Int { return x }\nfunc go() { helper(1) }\n\nlet secret = \"PRIVATE_LITERAL\"\n", "Box.run", "func bad( {"},
	"dart":       {"main.dart", "import 'lib.dart'; class Box { int run(int x) { return helper(x); } } int helper(int x) {return x;} void go() { helper(1); }\nfinal secret = 'PRIVATE_LITERAL';", "Box.run", "void bad( {"},
	"sql":        {"main.sql", "CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT); CREATE VIEW active AS SELECT id FROM users; SELECT * FROM active;\nSELECT 'PRIVATE_LITERAL';", "users.name", "CREATE TABLE ("},
	"json":       {"main.json", "{\"service\": {\"token\": \"PRIVATE_LITERAL\", \"enabled\": true}, \"items\": [{\"id\":1}]}", "service.token", "{\"bad\":"},
	"yaml":       {"main.yaml", "service:\n  token: PRIVATE_LITERAL\n  enabled: true\nitems:\n  - id: 1\n", "service.token", "bad: ["},
	"toml":       {"main.toml", "[service]\ntoken = 'PRIVATE_LITERAL'\nenabled = true\n[[items]]\nid = 1\n", "service.token", "[bad"},
}

func TestCommonLanguagesStructurePrivacyAndSyntax(t *testing.T) {
	treeNode(t)
	for lang, s := range commonSamples {
		t.Run(lang, func(t *testing.T) {
			var files []File
			if lang == "xml" {
				files = []File{parseXML(parserInput{s.path, s.code}), parseXML(parserInput{s.path, s.bad})}
			} else {
				result, err := parseTrees(context.Background(), "", lang, []parserInput{{s.path, s.code}, {"bad/" + s.path, s.bad}})
				if err != nil {
					t.Fatal(err)
				}
				files = result.Files
			}
			if files[0].ParseError != "" || files[1].ParseError == "" || len(files[1].Symbols) != 0 {
				t.Fatalf("parse %+v", files)
			}
			raw, _ := json.Marshal(files)
			if strings.Contains(string(raw), "PRIVATE_LITERAL") {
				t.Fatal("value/body leaked")
			}
			seen := map[string]bool{}
			found := false
			for _, symbol := range files[0].Symbols {
				if seen[symbol.ID] || symbol.Line < 1 || symbol.EndLine < symbol.Line {
					t.Fatalf("invalid node %+v", symbol)
				}
				seen[symbol.ID] = true
				if symbol.Name == s.wanted {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing %s: %+v", s.wanted, files[0].Symbols)
			}
			for _, symbol := range files[0].Symbols {
				if symbol.Parent != moduleID(s.path) && !seen[symbol.Parent] {
					t.Fatalf("orphan %+v", symbol)
				}
			}
		})
	}
}

func TestTwentyThreeLanguagesExpansionAndPersistence(t *testing.T) {
	treeNode(t)
	ctx := context.Background()
	root := t.TempDir()
	original := map[string]string{"keep.py": "def keep(): return 1\n", "main.go": "package main\nfunc Keep(){}", "main.js": "function keep(){}", "main.ts": "function keep():void{}", "Main.java": "class Main {static void keep(){}}", "Main.cs": "class Main {static void Keep(){}}", "main.rs": "fn keep(){}", "main.cpp": "void keep(){}"}
	for path, code := range original {
		source(t, root, "src/"+path, code)
	}
	for _, s := range commonSamples {
		source(t, root, "src/"+s.path, s.code)
	}
	r := readerFor(t, root)
	old, err := r.ScanWithOptions(ctx, []string{"src"}, nil, Index{}, ScanOptions{Languages: []string{"python", "go", "javascript", "typescript", "java", "csharp", "rust", "cpp"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.ScanWithOptions(ctx, old.Roots, nil, old, ScanOptions{Languages: []string{"all"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Languages) != 23 || got.Stats.Files != 23 || got.Stats.ParseErrors != 0 || got.Changes.Reused != 8 || got.Changes.Parsed != 15 {
		t.Fatalf("expansion %+v", got)
	}
	store := &publicationStore{plane: map[string]string{"human": "original", "@knowledge/record/solved": "original source-backed evidence"}}
	if _, err = Save(ctx, store, store.plane, got); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(store.plane)
	if err != nil {
		t.Fatal(err)
	}
	again, err := r.ScanWithOptions(ctx, loaded.Roots, nil, loaded, ScanOptions{Node: "missing-node", Python: "missing-python"})
	if err != nil || again.Changes.Reused != 23 || again.Changes.Parsed != 0 {
		t.Fatalf("reuse %+v %v", again.Changes, err)
	}
	source(t, root, "src/main.php", "<?php function changed() {}")
	if _, err = r.ScanWithOptions(ctx, loaded.Roots, nil, loaded, ScanOptions{Node: "missing-node"}); err == nil {
		t.Fatal("changed parser must be available")
	}
	retained, err := Load(store.plane)
	if err != nil || retained.Parser != loaded.Parser {
		t.Fatal("previous generation lost")
	}
	changed, err := r.ScanWithOptions(ctx, loaded.Roots, nil, loaded, ScanOptions{})
	if err != nil || changed.Changes.Parsed != 1 || changed.Changes.Reused != 22 {
		t.Fatalf("incremental %+v %v", changed.Changes, err)
	}
	rebuilt, err := r.ScanWithOptions(ctx, loaded.Roots, nil, loaded, ScanOptions{Rebuild: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Save(ctx, store, store.plane, rebuilt); err != nil {
		t.Fatal(err)
	}
	if store.plane["human"] != "original" || store.plane["@knowledge/record/solved"] != "original source-backed evidence" {
		t.Fatal("human evidence changed")
	}
}

//go:embed common-parser-provenance.json
var commonProvenance []byte

func TestCommonAssetsPinnedAndLanguageSelections(t *testing.T) {
	var provenance struct {
		Files map[string]struct {
			SHA256     string `json:"sha256"`
			GzipSHA256 string `json:"gzip_sha256"`
			Bytes      int    `json:"bytes"`
		} `json:"files"`
	}
	if err := json.Unmarshal(commonProvenance, &provenance); err != nil {
		t.Fatal(err)
	}
	if len(provenance.Files) != 14 {
		t.Fatal("incomplete provenance")
	}
	for name, p := range provenance.Files {
		raw, err := treeAsset(name)
		if err != nil || len(raw) != p.Bytes || fmt.Sprintf("%x", sha256.Sum256(raw)) != p.SHA256 {
			t.Fatalf("asset %s: %v", name, err)
		}
		compressed, _ := treeBundles.ReadFile(name + ".gz")
		if fmt.Sprintf("%x", sha256.Sum256(compressed)) != p.GzipSHA256 {
			t.Fatal("compressed asset mismatch")
		}
	}
	for lang, s := range commonSamples {
		if languageOf(s.path) != lang {
			t.Fatalf("extension %s", s.path)
		}
		if !strings.Contains(ParserLicenses(), lang+" grammar") && lang != "xml" {
			t.Fatal("missing attribution " + lang)
		}
	}
	langs, err := normalizeLanguages([]string{"all", "shell", "ps1", "yml"})
	if err != nil || len(langs) != 23 {
		t.Fatalf("selection %v %v", langs, err)
	}
	if _, err := normalizeLanguages([]string{"not-a-language"}); err == nil {
		t.Fatal("unknown selection allowed")
	}
	if languageOf("source.C") != "cpp" || languageOf("source.c") != "c" || languageOf("header.h") != "cpp" {
		t.Fatal("C/C++ convention changed")
	}
}

func TestCommonParserIsolationPathsAndConservativeCalls(t *testing.T) {
	treeNode(t)
	root := t.TempDir()
	marker := filepath.Join(root, "executed")
	preload := filepath.Join(root, "preload.cjs")
	if err := os.WriteFile(preload, []byte("require('fs').writeFileSync("+fmt.Sprintf("%q", marker)+",'unsafe');"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NODE_OPTIONS", "--require="+preload)
	samples := []struct{ lang, path, code string }{
		{"html", "page.html", `<main><script src="./main.js"></script><img src="https://host.invalid/private?token=SECRET_VALUE"><a href="../../outside.html">Link</a></main>`},
		{"php", "main.php", `<?php include 'lib.php'; function helper(){} function run(){helper();} if(true){function conditional(){}}`},
		{"c", "main.c", `int helper(void){return 1;} int run(void){return helper();} int shadow(int (*helper)(void)){return helper();}`},
		{"bash", "main.sh", `source ./lib.sh
helper(){ echo ok; }
run(){ helper; }
`},
		{"powershell", "main.ps1", `. ./lib.ps1
function Run { Helper }
`},
		{"dart", "main.dart", `import 'lib.dart'; void go(){ helper(); }`},
	}
	index := Index{Files: []File{{Path: "main.js", Language: "javascript"}, {Path: "lib.php", Language: "php"}, {Path: "lib.sh", Language: "bash"}, {Path: "lib.ps1", Language: "powershell"}, {Path: "lib.dart", Language: "dart"}}}
	for _, s := range samples {
		out, err := parseTrees(context.Background(), "", s.lang, []parserInput{{s.path, s.code}})
		if err != nil {
			t.Fatal(err)
		}
		f := out.Files[0]
		f.Language = s.lang
		if f.ParseError != "" {
			t.Fatal(f.ParseError)
		}
		index.Files = append(index.Files, f)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("preload executed")
	}
	links := map[string]string{}
	for _, rel := range Relations(index) {
		links[rel.Source+"|"+rel.Name] = rel.Target
		if strings.Contains(rel.Name, "SECRET_VALUE") {
			t.Fatal("external URL stored")
		}
	}
	for key, want := range map[string]string{"page.html::main.script|./main.js": "main.js::", "main.php::|lib.php": "lib.php::", "main.c::run|helper": "main.c::helper", "main.c::shadow|helper": "", "main.sh::|./lib.sh": "lib.sh::", "main.ps1::|./lib.ps1": "lib.ps1::", "main.dart::|lib.dart": "lib.dart::", "main.php::run|helper": "", "main.sh::run|helper": "", "main.dart::go|helper": ""} {
		if links[key] != want {
			t.Errorf("%s -> %q want %q", key, links[key], want)
		}
	}
	for _, rel := range Relations(index) {
		if rel.Name == "../../outside.html" && rel.Target != "" {
			t.Fatal("path escape resolved")
		}
	}
}

func TestXMLRejectsMalformedAndExternalEntities(t *testing.T) {
	for _, code := range []string{"", "<a/><b/>", "outside<a/>", "<a>", `<a>&private;</a>`, `<!DOCTYPE a [<!ENTITY private SYSTEM "file:///private/secret">]><a>&private;</a>`} {
		got := parseXML(parserInput{"main.xml", code})
		if got.ParseError == "" || len(got.Symbols) != 0 {
			t.Fatalf("invalid XML accepted: %+v", got)
		}
	}
	got := parseXML(parserInput{"main.xml", `<!DOCTYPE a SYSTEM "https://host.invalid/private"><a token="PRIVATE_LITERAL"><b/></a>`})
	raw, _ := json.Marshal(got)
	if got.ParseError != "" || strings.Contains(string(raw), "PRIVATE_LITERAL") || strings.Contains(string(raw), "host.invalid") {
		t.Fatalf("XML host/value exposure: %s", raw)
	}
}
