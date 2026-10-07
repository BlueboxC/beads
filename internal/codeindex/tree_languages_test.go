package codeindex

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func treeNode(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("operator Node unavailable")
	}
}

func TestTreeLanguagesStructureAndConservativeCalls(t *testing.T) {
	treeNode(t)
	root := t.TempDir()
	sources := map[string]string{
		"j/Util.java": `package demo; public class Util { public static int twice(int x) { return x*2; } }`,
		"j/Main.java": `package app; import demo.Util; class Main { static int run() { return Util.twice(1); } static int local() { return run(); } static void shadow(Object Util) { Util.twice(1); } int instance() { return 1; } int use() { return instance(); } }`,
		"cs/Main.cs":  `using System; namespace Demo; class Main { public static int Twice(int x)=>x*2; static int Run()=>Twice(1); static int Qualified()=>Main.Twice(1); static void Shadow(Func<int,int> Twice) { Twice(1); } int Instance()=>1; int Use()=>Instance(); }`,
		"rs/main.rs":  `mod util; struct Box {} impl Box { fn make() -> Box { Box {} } fn run(&self) { helper(); } } fn helper() {} fn main() { helper(); Box::make(); } fn shadow(helper: fn()) { helper(); }`,
		"rs/util.rs":  `pub fn twice(x:i32)->i32 { x*2 }`,
		"cpp/util.hpp": `#pragma once
namespace util { inline int twice(int x) { return x*2; } }`,
		"cpp/main.cpp": `#include "util.hpp"
namespace app { int helper() { return 1; } int run() { return helper(); } struct Box { static int make() { return 1; } int instance() { return 2; } }; int create() { return Box::make(); } int shadow(int (*helper)()) { return helper(); } }`,
	}
	for path, data := range sources {
		source(t, root, path, data)
	}
	r := readerFor(t, root)
	got, err := r.ScanWithOptions(context.Background(), []string{"j", "cs", "rs", "cpp"}, nil, Index{}, ScanOptions{Languages: []string{"java", "c#", "rust", "c++"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Stats.Files != 7 || got.Stats.ParseErrors != 0 {
		t.Fatalf("scan: %+v", got.Stats)
	}
	ids := map[string]bool{}
	for _, file := range got.Files {
		for _, s := range file.Symbols {
			if ids[s.ID] || s.Line < 1 || s.EndLine < s.Line {
				t.Fatalf("bad symbol %+v", s)
			}
			ids[s.ID] = true
		}
	}
	for _, id := range []string{"j/Main.java::Main.run", "cs/Main.cs::Demo.Main.Twice", "rs/main.rs::Box.make", "cpp/main.cpp::app.Box.make"} {
		if !ids[id] {
			t.Errorf("missing %s", id)
		}
	}
	links := map[string]string{}
	for _, rel := range Relations(got) {
		links[rel.Source+"|"+rel.Name] = rel.Target
	}
	for key, want := range map[string]string{
		"j/Main.java::Main.run|Util.twice":           "j/Util.java::Util.twice",
		"j/Main.java::Main.local|run":                "j/Main.java::Main.run",
		"cs/Main.cs::Demo.Main.Run|Twice":            "cs/Main.cs::Demo.Main.Twice",
		"cs/Main.cs::Demo.Main.Qualified|Main.Twice": "cs/Main.cs::Demo.Main.Twice",
		"rs/main.rs::main|helper":                    "rs/main.rs::helper",
		"rs/main.rs::main|Box.make":                  "rs/main.rs::Box.make",
		"rs/main.rs::|util":                          "rs/util.rs::",
		"cpp/main.cpp::|util.hpp":                    "cpp/util.hpp::",
		"cpp/main.cpp::app.run|helper":               "cpp/main.cpp::app.helper",
		"cpp/main.cpp::app.create|Box.make":          "cpp/main.cpp::app.Box.make",
	} {
		if links[key] != want {
			t.Errorf("%s => %q want %q", key, links[key], want)
		}
	}
	for _, key := range []string{"j/Main.java::Main.shadow|Util.twice", "j/Main.java::Main.use|instance", "cs/Main.cs::Demo.Main.Shadow|Twice", "cs/Main.cs::Demo.Main.Use|Instance", "rs/main.rs::shadow|helper", "cpp/main.cpp::app.shadow|helper"} {
		if links[key] != "" {
			t.Errorf("unsafe resolution %s => %s", key, links[key])
		}
	}
}

func TestTreeLanguagesErrorsOverloadsAndNonExecution(t *testing.T) {
	treeNode(t)
	ctx := context.Background()
	samples := map[string]struct{ path, code, bad string }{
		"java":   {"Main.java", `class Main { static void boom(){throw new RuntimeException("PRIVATE_LITERAL");} static void f(int x){} static void f(String x){} static void run(){f(1);} }`, `class Bad { void f( { "PRIVATE_LITERAL" }`},
		"csharp": {"Main.cs", `class Main { static void Boom(){throw new System.Exception("PRIVATE_LITERAL");} static void F(int x){} static void F(string x){} static void Run(){F(1);} }`, `class Bad { void F( { "PRIVATE_LITERAL" }`},
		"rust":   {"main.rs", `fn boom(){panic!("PRIVATE_LITERAL");} fn f(){} fn f(){} fn run(){f();}`, `fn broken( { "PRIVATE_LITERAL" }`},
		"cpp":    {"main.cpp", `void boom(){throw "PRIVATE_LITERAL";} void f(int x){} void f(float x){} void run(){f(1);}`, `void broken( { "PRIVATE_LITERAL" }`},
	}
	for language, s := range samples {
		t.Run(language, func(t *testing.T) {
			parsed, err := parseTrees(ctx, "", language, []parserInput{{s.path, s.code}, {"bad/" + s.path, s.bad}})
			if err != nil {
				t.Fatal(err)
			}
			if parsed.Files[0].ParseError != "" || parsed.Files[1].ParseError == "" || len(parsed.Files[1].Symbols) != 0 {
				t.Fatalf("parse %+v", parsed.Files)
			}
			encoded, _ := json.Marshal(parsed)
			if strings.Contains(string(encoded), "PRIVATE_LITERAL") {
				t.Fatal("source literal leaked")
			}
			file := parsed.Files[0]
			file.Language = language
			seen := map[string]bool{}
			for _, symbol := range file.Symbols {
				if seen[symbol.ID] {
					t.Fatalf("duplicate graph node %s", symbol.ID)
				}
				seen[symbol.ID] = true
			}
			for _, rel := range Relations(Index{Files: []File{file}}) {
				if (rel.Name == "f" || rel.Name == "F") && rel.Target != "" {
					t.Fatalf("overload guessed %+v", rel)
				}
			}
		})
	}
}

func TestEightLanguageIncrementalPersistence(t *testing.T) {
	treeNode(t)
	root := t.TempDir()
	for path, data := range map[string]string{"keep.py": "def keep(): return 1\n", "main.go": "package main\nfunc Keep(){}\n", "main.js": "function keep(){}", "main.ts": "function keep():void{}", "Main.java": "class Main {static void keep(){}}", "Main.cs": "class Main {static void Keep(){}}", "main.rs": "fn keep(){}", "main.cpp": "void keep(){}"} {
		source(t, root, "src/"+path, data)
	}
	r := readerFor(t, root)
	ctx := context.Background()
	old, err := r.ScanWithOptions(ctx, []string{"src"}, nil, Index{}, ScanOptions{Languages: []string{"python", "go", "javascript", "typescript"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.ScanWithOptions(ctx, old.Roots, nil, old, ScanOptions{Languages: []string{"all"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Stats.Files != 8 || got.Stats.ParseErrors != 0 || got.Changes.Reused != 4 || got.Changes.Parsed != 4 {
		t.Fatalf("expansion %+v", got)
	}
	store := &publicationStore{plane: map[string]string{"human": "preserve", "@knowledge/assertion": "old evidence"}}
	if _, err = Save(ctx, store, store.plane, got); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(store.plane)
	if err != nil {
		t.Fatal(err)
	}
	again, err := r.ScanWithOptions(ctx, got.Roots, nil, loaded, ScanOptions{Python: "missing-python", Node: "missing-node"})
	if err != nil || again.Changes.Parsed != 0 || again.Changes.Reused != 8 {
		t.Fatalf("cache %+v %v", again.Changes, err)
	}
	if store.plane["human"] != "preserve" || store.plane["@knowledge/assertion"] != "old evidence" {
		t.Fatal("human data changed")
	}
	source(t, root, "src/main.rs", "fn changed(){}")
	fresh, err := r.ScanWithOptions(ctx, got.Roots, nil, loaded, ScanOptions{})
	if err != nil || fresh.Changes.Parsed != 1 || fresh.Changes.Reused != 7 {
		t.Fatalf("incremental %+v %v", fresh.Changes, err)
	}
	store.failManifest = true
	if _, err = Save(ctx, store, store.plane, fresh); err == nil {
		t.Fatal("expected publication failure")
	}
	retained, err := Load(store.plane)
	if err != nil || retained.Files[6].SHA256 != loaded.Files[6].SHA256 {
		t.Fatalf("previous generation lost %v", err)
	}
}

func TestTreeAssetsAndLanguageSelection(t *testing.T) {
	for _, lang := range []string{"java", "csharp", "rust", "cpp"} {
		if _, err := treeAsset("tree-sitter-" + grammarName(lang) + ".wasm"); err != nil {
			t.Fatal(err)
		}
		if parserHash(lang) == parserHash("typescript") || parserHash(lang) == parserHash("python") {
			t.Fatal("parser identity collision")
		}
	}
	for file, language := range map[string]string{"Main.java": "java", "Main.cs": "csharp", "main.rs": "rust", "main.cpp": "cpp", "main.CXX": "cpp", "include.H": "cpp", "include.hpp": "cpp", "main.C": "cpp"} {
		if languageOf(file) != language {
			t.Errorf("%s language", file)
		}
	}
	langs, err := normalizeLanguages([]string{"all", "C#", "c++"})
	if err != nil || len(langs) != 8 {
		t.Fatalf("selection %v %v", langs, err)
	}
	if !strings.Contains(ParserLicenses(), "Java grammar") || !strings.Contains(ParserLicenses(), "Ayman Nadeem") || !strings.Contains(ParserLicenses(), "TypeScript") {
		t.Fatal("attribution missing")
	}
}

//go:embed tree-sitter-provenance.json
var treeProvenance []byte

func TestTreeAssetsMatchPinnedProvenance(t *testing.T) {
	var provenance struct {
		Files map[string]struct {
			SHA256 string `json:"sha256"`
			Bytes  int    `json:"bytes"`
		} `json:"files"`
	}
	if err := json.Unmarshal(treeProvenance, &provenance); err != nil {
		t.Fatal(err)
	}
	if len(provenance.Files) != 6 {
		t.Fatal("incomplete provenance")
	}
	for name, entry := range provenance.Files {
		raw, err := treeAsset(name)
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) != entry.Bytes || fmt.Sprintf("%x", sha256.Sum256(raw)) != entry.SHA256 {
			t.Fatalf("asset mismatch %s", name)
		}
	}
}

func TestTreeParserIsolationAndBuildUncertainty(t *testing.T) {
	treeNode(t)
	root := t.TempDir()
	marker := filepath.Join(root, "executed")
	preload := filepath.Join(root, "preload.cjs")
	if err := os.WriteFile(preload, []byte("require('fs').writeFileSync("+fmt.Sprintf("%q", marker)+",'unsafe');"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NODE_OPTIONS", "--require="+preload)
	rust, err := parseTrees(context.Background(), "", "rust", []parserInput{{"main.rs", `#[cfg(feature="hidden")] fn maybe(){} fn main(){maybe();} struct S{} trait T{} impl T for S {fn make(){}} fn run(){S::make();}`}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("operator environment injection executed")
	}
	rust.Files[0].Language = "rust"
	for _, rel := range Relations(Index{Files: rust.Files}) {
		if (rel.Name == "maybe" || rel.Name == "S.make") && rel.Target != "" {
			t.Fatalf("conditional/trait target guessed %+v", rel)
		}
	}
	cpp, err := parseTrees(context.Background(), "", "cpp", []parserInput{{"main.cpp", `#define named(x) x
int named(int x){return x;}
int run(){return named(1);}`}})
	if err != nil {
		t.Fatal(err)
	}
	cpp.Files[0].Language = "cpp"
	for _, rel := range Relations(Index{Files: cpp.Files}) {
		if rel.Name == "named" && rel.Target != "" {
			t.Fatal("macro-expanded target guessed")
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := parseTrees(canceled, "", "java", []parserInput{{"Main.java", "class Main {}"}}); err == nil {
		t.Fatal("cancellation ignored")
	}
	if _, err := parseTrees(context.Background(), "missing-node", "java", []parserInput{{"Main.java", "class Main {}"}}); err == nil {
		t.Fatal("missing operator accepted")
	}
}

func TestTreeNestedNamespacesAndBindingShadowing(t *testing.T) {
	treeNode(t)
	scenarios := []struct{ language, path, code, owner, call string }{
		{"java", "Main.java", `class Main {static class Util {static void twice(){}} void run(){try {} catch(RuntimeException Util){Util.twice();}}}`, "Main.run", "Util.twice"},
		{"rust", "main.rs", `struct S{f: fn()} fn f(){} fn run(s:S){let S{f}=s;f();}`, "run", "f"},
		{"cpp", "main.cpp", `int helper(){return 1;} int run(){int (*helper)();return helper();}`, "run", "helper"},
	}
	for _, s := range scenarios {
		t.Run(s.language, func(t *testing.T) {
			got, err := parseTrees(context.Background(), "", s.language, []parserInput{{s.path, s.code}})
			if err != nil {
				t.Fatal(err)
			}
			file := got.Files[0]
			file.Language = s.language
			if file.ParseError != "" {
				t.Fatal(file.ParseError)
			}
			found := false
			for _, rel := range Relations(Index{Files: []File{file}}) {
				if rel.Source == s.path+"::"+s.owner && rel.Name == s.call {
					found = true
					if rel.Target != "" {
						t.Fatalf("shadowed binding guessed %+v", rel)
					}
				}
			}
			if !found {
				t.Fatal("shadowed call lost")
			}
		})
	}
	got, err := parseTrees(context.Background(), "", "cpp", []parserInput{{"main.cpp", `namespace app::detail { template<class T> struct Holder {static T wrap(T value){return value;} }; int helper(){return 1;} int run(){return helper();} }`}})
	if err != nil {
		t.Fatal(err)
	}
	file := got.Files[0]
	file.Language = "cpp"
	ids := map[string]bool{}
	for _, s := range file.Symbols {
		ids[s.ID] = true
	}
	for _, id := range []string{"main.cpp::app.detail", "main.cpp::app.detail.Holder.wrap", "main.cpp::app.detail.run"} {
		if !ids[id] {
			t.Fatalf("nested/template symbol missing %s: %+v", id, file)
		}
	}
	found := false
	for _, rel := range Relations(Index{Files: []File{file}}) {
		if rel.Source == "main.cpp::app.detail.run" && rel.Name == "helper" {
			found = true
			if rel.Target != "main.cpp::app.detail.helper" {
				t.Fatalf("namespace resolution %+v", rel)
			}
		}
	}
	if !found {
		t.Fatal("namespace call missing")
	}
}
