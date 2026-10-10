package codeindex

import (
	"path"
	"strings"
	"unicode"
)

func scriptImportPath(from, name string, files map[string]File) string {
	if !strings.HasPrefix(name, "./") && !strings.HasPrefix(name, "../") {
		return ""
	}
	base := path.Clean(path.Join(path.Dir(from), name))
	if base == ".." || strings.HasPrefix(base, "../") || strings.HasPrefix(base, "/") {
		return ""
	}
	candidates := []string{base}
	if path.Ext(base) == "" {
		for _, ext := range []string{".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs"} {
			candidates = append(candidates, base+ext, base+"/index"+ext)
		}
	}
	// NodeNext explicitly authored .js imports can name TypeScript sources.
	if _, exists := files[base]; !exists {
		switch path.Ext(base) {
		case ".js":
			candidates = append(candidates, strings.TrimSuffix(base, ".js")+".ts", strings.TrimSuffix(base, ".js")+".tsx")
		case ".mjs":
			candidates = append(candidates, strings.TrimSuffix(base, ".mjs")+".mts")
		case ".cjs":
			candidates = append(candidates, strings.TrimSuffix(base, ".cjs")+".cts")
		}
	}
	found := ""
	for _, candidate := range candidates {
		if f, ok := files[candidate]; ok && (fileLanguage(f) == "javascript" || fileLanguage(f) == "typescript") {
			if found != "" && found != candidate {
				return ""
			}
			found = candidate
		}
	}
	return found
}
func nativeRelations(index Index) []Relation {
	files := map[string]File{}
	symbols := map[string]string{}
	counts := map[string]int{}
	classes := map[string]bool{}
	kinds := map[string]string{}
	goPackages := map[string][]File{}
	goNames := map[string]string{}
	ambiguousPackages := map[string]bool{}
	exports := map[string]string{}
	for _, file := range index.Files {
		language := fileLanguage(file)
		if language == "python" || language == "xml" || treeLanguage(language) {
			continue
		}
		files[file.Path] = file
		if language == "go" && file.Package != "" {
			if prior := goNames[file.Module]; prior != "" && prior != file.Package {
				ambiguousPackages[file.Module] = true
			}
			goPackages[file.Module] = append(goPackages[file.Module], file)
			goNames[file.Module] = file.Package
		}
		for _, s := range file.Symbols {
			kinds[s.ID] = s.Kind
			classes[s.ID] = s.Kind == "class" || s.Kind == "interface"
		}
	}
	for _, file := range files {
		for _, s := range file.Symbols {
			if fileLanguage(file) != "go" && s.Kind == "method" && kinds[s.Parent] == "class" && !s.Static {
				continue
			}
			key := s.ID
			counts[key]++
			symbols[key] = s.ID
			if fileLanguage(file) == "go" {
				key = file.Module + "." + s.Name
				counts[key]++
				symbols[key] = s.ID
			}
		}
	}
	for key, count := range counts {
		if count != 1 {
			delete(symbols, key)
		}
	}
	for _, file := range files {
		for name, qualified := range file.Exports {
			if target := symbols[file.Path+"::"+qualified]; qualified != "" && target != "" {
				exports[file.Path+"::"+name] = target
			}
		}
	}
	result := []Relation{}
	for _, file := range index.Files {
		language := fileLanguage(file)
		if language == "python" || language == "xml" || treeLanguage(language) {
			continue
		}
		bindings := map[string]map[string]Import{}
		importPaths := map[string]string{}
		for _, item := range file.Imports {
			target, alias := "", item.Alias
			if item.Uncertain {
				result = append(result, Relation{Source: item.Owner, Kind: "imports", Name: item.Name, Path: file.Path, Line: item.Line, Resolution: "unresolved"})
				if bindings[item.Owner] == nil {
					bindings[item.Owner] = map[string]Import{}
				}
				item.Name = ""
				bindings[item.Owner][alias] = item
				continue
			}
			if language == "go" {
				pkg := goPackages["go:"+item.Name]
				if len(pkg) > 0 && !ambiguousPackages["go:"+item.Name] {
					target = moduleID(pkg[0].Path)
					importPaths[item.Name] = "go:" + item.Name
					if !item.ExplicitAlias {
						alias = goNames["go:"+item.Name]
					}
				}
			} else {
				if p := scriptImportPath(file.Path, item.Name, files); p != "" {
					importPaths[item.Name] = p
					target = moduleID(p)
					if item.Member != "" {
						target = exports[p+"::"+item.Member]
					}
				}
			}
			resolution := "unresolved"
			if target != "" {
				resolution = "static_reference"
			}
			result = append(result, Relation{Source: item.Owner, Target: target, Kind: "imports", Name: item.Name, Path: file.Path, Line: item.Line, Resolution: resolution})
			if item.TypeOnly || alias == "" || alias == "_" || alias == "." {
				continue
			}
			item.Alias = alias
			if bindings[item.Owner] == nil {
				bindings[item.Owner] = map[string]Import{}
			}
			if _, exists := bindings[item.Owner][alias]; exists {
				item.Name = ""
			}
			bindings[item.Owner][alias] = item
		}
		blocked := func(scope, name string) bool {
			for _, v := range file.Blocked[scope] {
				if v == name || v == "*" {
					return true
				}
			}
			return false
		}
		for _, call := range file.Calls {
			target := ""
			first := strings.Split(call.Name, ".")[0]
			for scope := call.Owner; scope != ""; scope = parentScope(scope) {
				if language == "go" && scope != call.Owner && scope != moduleID(file.Path) {
					continue
				}
				if scope != call.Owner && classes[scope] {
					continue
				}
				if blocked(scope, first) {
					break
				}
				if item, found := bindings[scope][first]; found {
					prefix := importPaths[item.Name]
					rest := strings.TrimPrefix(call.Name, first)
					if language == "go" {
						if rest != "" {
							member := strings.TrimPrefix(rest, ".")
							runes := []rune(member)
							if len(runes) > 0 && unicode.IsUpper(runes[0]) {
								target = symbols[prefix+rest]
							}
						}
					} else if prefix != "" {
						if item.Member != "" {
							base := exports[prefix+"::"+item.Member]
							if rest == "" {
								target = base
							} else {
								target = symbols[base+rest]
							}
						} else if item.CommonJS && rest == "" {
							target = exports[prefix+"::default"]
						} else if rest != "" {
							target = exports[prefix+"::"+strings.TrimPrefix(rest, ".")]
						}
					}
					break
				}
				if language == "go" && scope == moduleID(file.Path) {
					target = symbols[file.Module+"."+call.Name]
					break
				}
				qualifier := strings.SplitN(scope, "::", 2)[1]
				if qualifier != "" {
					qualifier += "."
				}
				if candidate := symbols[file.Path+"::"+qualifier+first]; candidate != "" {
					target = symbols[file.Path+"::"+qualifier+call.Name]
					break
				}

			}
			resolution := "unresolved"
			if target != "" {
				resolution = "static_reference"
			}
			result = append(result, Relation{Source: call.Owner, Target: target, Kind: "calls", Name: call.Name, Path: file.Path, Line: call.Line, Resolution: resolution})
		}
	}
	return result
}
