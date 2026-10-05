package codeindex

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestMultiLanguageScanKeepsPythonAndConservativeReferences(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("operator Node unavailable")
	}
	root := t.TempDir()
	source(t, root, "go.mod", "module example.local/app\n\ngo 1.26\n")
	source(t, root, "py/legacy.py", "def keep(): return 1\n")
	source(t, root, "pkg/a.go", "package pkg\nfunc First() int { return Other() }\nfunc Shadow(Other func() int) int { return Other() }\ntype Reader struct{}\nfunc (r *Reader) Read() int {return Other()}\n")
	source(t, root, "pkg/b.go", "package pkg\nfunc Other() int {return First()}\n")
	source(t, root, "use/main.go", "package use\nimport renamed \"example.local/app/pkg\"\nfunc Run() int {return renamed.First()}\nfunc Shadow(renamed interface{}) {renamed.First()}\n")
	source(t, root, "js/a.ts", "export interface Payload { value: number }\nexport function twice(x: number): number { return x*2; }\nexport class Thing { run(){ return twice(1); } }\n")
	source(t, root, "js/b.js", "const {twice: f} = require('./a');\nfunction main(){return f(1)}\nfunction shadow(f){return f(2)}\nmodule.exports = { main };\nthrow Error('must never run');\n")
	source(t, root, "js/ui.tsx", "import {twice} from './a';\nexport const Widget = (p: {n: number}) => <span>{twice(p.n)}</span>;\n")
	r := readerFor(t, root)
	ctx := context.Background()
	legacy, err := r.Scan(ctx, []string{"py"}, nil, Index{}, false, "")
	if err != nil {
		t.Fatal(err)
	}
	mixed, err := r.ScanWithOptions(ctx, []string{"py", "pkg", "use", "js"}, nil, legacy, ScanOptions{Languages: []string{"python", "go", "javascript", "typescript"}})
	if err != nil {
		t.Fatal(err)
	}
	if mixed.Version != 3 || mixed.Stats.Files != 7 || mixed.Changes.Reused != 1 || mixed.Stats.ParseErrors != 0 {
		t.Fatalf("bad mixed scan: %+v", mixed)
	}
	links := map[string]string{}
	for _, r := range Relations(mixed) {
		if r.Kind == "calls" {
			links[r.Source+"|"+r.Name] = r.Target
		}
	}
	for key, target := range map[string]string{"pkg/a.go::First|Other": "pkg/b.go::Other", "pkg/b.go::Other|First": "pkg/a.go::First", "use/main.go::Run|renamed.First": "pkg/a.go::First", "js/b.js::main|f": "js/a.ts::twice", "js/ui.tsx::Widget|twice": "js/a.ts::twice", "js/a.ts::Thing.run|twice": "js/a.ts::twice"} {
		if links[key] != target {
			t.Errorf("%s -> %q; want %s", key, links[key], target)
		}
	}
	for _, key := range []string{"pkg/a.go::Shadow|Other", "use/main.go::Shadow|renamed.First", "js/b.js::shadow|f"} {
		if links[key] != "" {
			t.Errorf("shadowed call guessed: %s", key)
		}
	}
	store := &publicationStore{plane: map[string]string{"human": "preserved"}}
	if _, err := Save(ctx, store, store.plane, mixed); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(store.plane)
	if err != nil || loaded.Version != 3 || store.plane["human"] != "preserved" {
		t.Fatalf("persistence: %v", err)
	}
	unchanged, err := r.ScanWithOptions(ctx, mixed.Roots, nil, loaded, ScanOptions{Python: "missing-python", Node: "missing-node"})
	if err != nil || unchanged.Changes.Reused != 7 || unchanged.Changes.Parsed != 0 {
		t.Fatalf("unchanged should need no interpreters: %v %+v", err, unchanged.Changes)
	}
	source(t, root, "go.mod", "module example.local/changed\n\ngo 1.26\n")
	if got := r.Refresh(loaded); len(got.Warnings) == 0 {
		t.Fatal("go.mod mapping drift not visible")
	}
}

func TestNativeAmbiguitySyntaxAndSourceNonExecution(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("operator Node unavailable")
	}
	root := t.TempDir()
	source(t, root, "go.mod", "module example.local/app\n\ngo 1.26\n")
	source(t, root, "pkg/a.go", "package pkg\nfunc Same(){}\nfunc Caller(){ Same() }\nfunc init(){ panic(\"must never execute\") }\n")
	source(t, root, "pkg/b.go", "package pkg\nfunc Same(){}\n")
	source(t, root, "js/a.ts", "export function foo(){}\n")
	source(t, root, "js/a.js", "exports.foo = foo; function foo(){}\n")
	source(t, root, "js/b.ts", "import {foo} from './a';\nfoo();\n")
	source(t, root, "js/bad.ts", "const SECRET = 'private-literal'; function broken( {\n")
	source(t, root, "pkg/bad.go", "package pkg\nfunc broken( {\n")
	r := readerFor(t, root)
	index, err := r.ScanWithOptions(context.Background(), []string{"pkg", "js"}, nil, Index{}, ScanOptions{Languages: []string{"go", "javascript", "typescript"}})
	if err != nil {
		t.Fatal(err)
	}
	if index.Stats.ParseErrors != 2 {
		t.Fatalf("syntax errors: %+v", index.Stats)
	}
	for _, rel := range Relations(index) {
		if rel.Source == "pkg/a.go::Caller" || rel.Source == "js/b.ts::" {
			if rel.Target != "" {
				t.Fatalf("ambiguous reference guessed: %+v", rel)
			}
		}
	}
	for _, file := range index.Files {
		if strings.Contains(file.ParseError, "SECRET") || strings.Contains(file.ParseError, "private-literal") {
			t.Fatal("source excerpt leaked")
		}
	}
	if err := os.Rename(root+"/js/b.ts", root+"/js/renamed.ts"); err != nil {
		t.Fatal(err)
	}
	next, err := r.ScanWithOptions(context.Background(), index.Roots, nil, index, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Changes.Renamed) != 1 {
		t.Fatal("rename not recorded")
	}
	store := &publicationStore{plane: map[string]string{"human": "not renewed"}}
	if _, err := Save(context.Background(), store, store.plane, index); err != nil {
		t.Fatal(err)
	}
	store.failManifest = true
	if _, err := Save(context.Background(), store, store.plane, next); err == nil {
		t.Fatal("interrupted publication succeeded")
	}
	prior, err := Load(store.plane)
	if err != nil || prior.Stats.Files != index.Stats.Files || store.plane["human"] != "not renewed" {
		t.Fatal("interrupted scan changed human/previous state")
	}
}

func TestNativeRequireExportsTypesAndReceivers(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("operator Node unavailable")
	}
	root := t.TempDir()
	source(t, root, "a.ts", "export function foo(){}\nexport interface Shape {}\nexport class Cls { static run(){foo()} instance(){foo()} }\n")
	source(t, root, "default.cjs", "function actual(){}; module.exports = actual;\n")
	source(t, root, "use.ts", "import {Cls} from './a'; import type {Shape} from './a';\nconst f=require('./default.cjs');\nfunction run(unknown: any){ f(); Cls.run(); Cls.instance(); unknown.run(); Shape(); }\nfunction shadow(require: any){const {foo}=require('./a'); foo()}\n")
	source(t, root, "hoist.js", "const {foo}=require('./a'); function require(x){return {foo:()=>0}}; foo();\n")
	source(t, root, "exports.js", "function foo(){}; function nested(){exports.foo=foo}; const module={exports:{}}; module.exports=foo;\n")
	source(t, root, "duplicate.ts", "function a(){}; function b(){}; export {a as same}; export {b as same};\n")
	source(t, root, "duplicate-use.ts", "import {same} from './duplicate'; same();\n")
	index, err := readerFor(t, root).ScanWithOptions(context.Background(), []string{"a.ts", "default.cjs", "use.ts", "hoist.js", "exports.js", "duplicate.ts", "duplicate-use.ts"}, nil, Index{}, ScanOptions{Languages: []string{"javascript", "typescript"}})
	if err != nil {
		t.Fatal(err)
	}
	calls := map[string]string{}
	typeRelation := false
	for _, rel := range Relations(index) {
		if rel.Kind == "calls" {
			calls[rel.Source+"|"+rel.Name] = rel.Target
		}
		if rel.Source == "use.ts::" && rel.Kind == "imports" && rel.Target == "a.ts::Shape" {
			typeRelation = true
		}
	}
	for key, want := range map[string]string{"use.ts::run|f": "default.cjs::actual", "use.ts::run|Cls.run": "a.ts::Cls.run", "use.ts::run|Cls.instance": "", "use.ts::run|unknown.run": "", "use.ts::run|Shape": "", "use.ts::shadow|foo": "", "hoist.js::|foo": "", "duplicate-use.ts::|same": ""} {
		if calls[key] != want {
			t.Errorf("%s: %q want %q", key, calls[key], want)
		}
	}
	if !typeRelation {
		t.Error("type-only structural import lost")
	}
	for _, f := range index.Files {
		if f.Path == "exports.js" && len(f.Exports) != 0 {
			t.Fatal("shadowed/nested CommonJS exports guessed")
		}
	}
}
