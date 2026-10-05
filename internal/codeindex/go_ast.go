package codeindex

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"strconv"
	"strings"
)

// parseGo reads supplied text; it never loads packages or runs init/build tools.
func parseGo(input parserInput) File {
	out := File{Path: input.Path, Blocked: map[string][]string{}}
	positions := token.NewFileSet()
	tree, err := parser.ParseFile(positions, input.Path, input.Content, parser.AllErrors|parser.SkipObjectResolution)
	if err != nil {
		out.ParseError = "SyntaxError"
		if list, ok := err.(scanner.ErrorList); ok && len(list) > 0 {
			out.ParseError += fmt.Sprintf(" at line %d", list[0].Pos.Line)
		}
		return out
	}
	out.Package = tree.Name.Name
	owner := moduleID(input.Path)
	block := func(scope, name string) {
		if name != "" && name != "_" {
			out.Blocked[scope] = append(out.Blocked[scope], name)
		}
	}
	line := func(n ast.Node) int { return positions.Position(n.Pos()).Line }
	for _, item := range tree.Imports {
		name, err := strconv.Unquote(item.Path.Value)
		if err != nil {
			continue
		}
		alias := name[strings.LastIndex(name, "/")+1:]
		explicit := item.Name != nil
		if explicit {
			alias = item.Name.Name
		}
		out.Imports = append(out.Imports, Import{Owner: owner, Name: name, Alias: alias, Line: line(item), ExplicitAlias: explicit})
		if alias == "." {
			block(owner, "*")
		}
	}
	symbol := func(name, kind string, n ast.Node, parent string) {
		out.Symbols = append(out.Symbols, Symbol{ID: input.Path + "::" + name, Name: name, Kind: kind, Line: line(n), EndLine: positions.Position(n.End()).Line, Parent: parent})
	}
	for _, decl := range tree.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch item := spec.(type) {
				case *ast.TypeSpec:
					kind := "type"
					switch item.Type.(type) {
					case *ast.StructType:
						kind = "struct"
					case *ast.InterfaceType:
						kind = "interface"
					}
					symbol(item.Name.Name, kind, item, owner)
				case *ast.ValueSpec:
					for _, name := range item.Names {
						block(owner, name.Name)
					}
				}
			}
		case *ast.FuncDecl:
			name, kind := d.Name.Name, "function"
			if d.Recv != nil && len(d.Recv.List) > 0 {
				receiver := goName(d.Recv.List[0].Type)
				if receiver != "" {
					name = receiver + "." + name
				}
				kind = "method"
			}
			scope := input.Path + "::" + name
			symbol(name, kind, d, owner)
			for _, list := range []*ast.FieldList{d.Recv, d.Type.Params, d.Type.Results, d.Type.TypeParams} {
				if list != nil {
					for _, field := range list.List {
						for _, n := range field.Names {
							block(scope, n.Name)
						}
					}
				}
			}
			if d.Body != nil {
				ast.Inspect(d.Body, func(n ast.Node) bool {
					switch x := n.(type) {
					case *ast.FuncLit:
						return false
					case *ast.CallExpr:
						name := goName(x.Fun)
						if name == "" {
							name = "<dynamic>"
						}
						out.Calls = append(out.Calls, Call{Owner: scope, Name: name, Line: line(x)})
					case *ast.AssignStmt:
						for _, lhs := range x.Lhs {
							if id, ok := lhs.(*ast.Ident); ok {
								block(scope, id.Name)
							}
						}
					case *ast.ValueSpec:
						for _, id := range x.Names {
							block(scope, id.Name)
						}
					case *ast.RangeStmt:
						for _, expr := range []ast.Expr{x.Key, x.Value} {
							if id, ok := expr.(*ast.Ident); ok {
								block(scope, id.Name)
							}
						}
					}
					return true
				})
			}
		}
	}
	return out
}
func goName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		if base := goName(e.X); base != "" {
			return base + "." + e.Sel.Name
		}
	case *ast.StarExpr:
		return goName(e.X)
	case *ast.IndexExpr:
		return goName(e.X)
	case *ast.IndexListExpr:
		return goName(e.X)
	case *ast.ParenExpr:
		return goName(e.X)
	}
	return ""
}
