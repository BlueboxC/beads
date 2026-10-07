package codeindex

import (
	"path"
	"strings"
)

// treeRelations deliberately resolves only unique syntax-backed targets. It
// never evaluates build files, overloads, receivers, macros or crate roots.
func treeRelations(index Index) []Relation {
	files := map[string]File{}
	symbols := map[string]Symbol{}
	local := map[string]string{}
	qualified := map[string]string{}
	counts := map[string]int{}
	typeCounts := map[string]int{}
	for _, file := range index.Files {
		if !treeLanguage(fileLanguage(file)) {
			continue
		}
		files[file.Path] = file
		for _, symbol := range file.Symbols {
			key := file.Path + "::" + symbol.Name
			counts[key]++
			local[key] = symbol.ID
			symbols[symbol.ID] = symbol
			if fileLanguage(file) == "java" && file.Package != "" {
				qualifiedKey := file.Package + "." + symbol.Name
				typeCounts[qualifiedKey]++
				qualified[qualifiedKey] = symbol.ID
			}
		}
	}
	for key, count := range counts {
		if count != 1 {
			delete(local, key)
		}
	}
	for key, count := range typeCounts {
		if count != 1 {
			delete(qualified, key)
		}
	}
	callable := func(id string) string {
		s, found := symbols[id]
		if found && !s.Uncertain && (s.Kind == "function" || s.Kind == "method" && s.Static) {
			return id
		}
		return ""
	}
	result := []Relation{}
	for _, file := range index.Files {
		if !treeLanguage(fileLanguage(file)) {
			continue
		}
		bindings := map[string]map[string]string{}
		duplicates := map[string]map[string]bool{}
		for _, item := range file.Imports {
			target := ""
			if !item.Uncertain {
				switch fileLanguage(file) {
				case "java":
					target = qualified[item.Name]
				case "cpp":
					candidate := path.Join(path.Dir(file.Path), item.Name)
					if f, ok := files[candidate]; ok && fileLanguage(f) == "cpp" && !path.IsAbs(item.Name) && candidate != ".." && !strings.HasPrefix(candidate, "../") {
						target = moduleID(candidate)
					}
				case "rust":
					dir := path.Dir(file.Path)
					base := path.Base(file.Path)
					if base != "main.rs" && base != "lib.rs" && base != "mod.rs" {
						dir = path.Join(dir, strings.TrimSuffix(base, ".rs"))
					}
					scope := strings.TrimPrefix(item.Owner, moduleID(file.Path))
					if scope != "" {
						dir = path.Join(dir, strings.ReplaceAll(scope, ".", "/"))
					}
					candidates := []string{path.Join(dir, item.Name+".rs"), path.Join(dir, item.Name, "mod.rs")}
					for _, candidate := range candidates {
						if f, ok := files[candidate]; ok && fileLanguage(f) == "rust" {
							if target != "" {
								target = ""
								break
							}
							target = moduleID(candidate)
						}
					}
				}
			}
			resolution := "unresolved"
			if target != "" {
				resolution = "static_reference"
			}
			result = append(result, Relation{Source: item.Owner, Target: target, Kind: "imports", Name: item.Name, Path: file.Path, Line: item.Line, Resolution: resolution})
			if fileLanguage(file) == "java" && item.Alias != "" {
				if bindings[item.Owner] == nil {
					bindings[item.Owner] = map[string]string{}
					duplicates[item.Owner] = map[string]bool{}
				}
				if _, ok := bindings[item.Owner][item.Alias]; ok {
					duplicates[item.Owner][item.Alias] = true
				}
				bindings[item.Owner][item.Alias] = target
			}
		}
		for _, call := range file.Calls {
			target := ""
			first := strings.Split(call.Name, ".")[0]
			for scope := call.Owner; scope != ""; scope = parentScope(scope) {
				blocked := false
				for _, name := range file.Blocked[scope] {
					if name == "*" || name == first {
						blocked = true
						break
					}
				}
				if blocked {
					break
				}
				if base, ok := bindings[scope][first]; ok {
					if !duplicates[scope][first] && base != "" {
						if strings.Contains(call.Name, ".") {
							q := symbols[base].Name + strings.TrimPrefix(call.Name, first)
							prefix := strings.SplitN(base, "::", 2)[0]
							target = callable(local[prefix+"::"+q])
						} else {
							target = callable(base)
						}
					}
					break
				}
				name := strings.TrimPrefix(scope, moduleID(file.Path))
				if name != "" {
					name += "."
				}
				key := file.Path + "::" + name + call.Name
				if candidate := local[key]; candidate != "" {
					target = callable(candidate)
					break
				}
				// Any declaration of the leading name prevents escaping to an outer scope.
				if counts[file.Path+"::"+name+first] > 0 || counts[key] > 0 {
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
