package codeindex

import (
	"fmt"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/graphview"
	"github.com/steveyegge/beads/internal/knowledge"
)

type Relation struct {
	Source     string `json:"source"`
	Target     string `json:"target,omitempty"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Path       string `json:"path"`
	Line       int    `json:"line"`
	Resolution string `json:"resolution"`
}

func moduleID(path string) string { return path + "::" }
func parentScope(owner string) string {
	parts := strings.SplitN(owner, "::", 2)
	if len(parts) != 2 || parts[1] == "" {
		return ""
	}
	if dot := strings.LastIndex(parts[1], "."); dot >= 0 {
		return parts[0] + "::" + parts[1][:dot]
	}
	return parts[0] + "::"
}
func importedName(file File, item Import) string {
	name := item.Name
	if item.Level > 0 {
		pkg := strings.Split(file.Module, ".")
		if !strings.HasSuffix(file.Path, "/__init__.py") {
			pkg = pkg[:len(pkg)-1]
		}
		if item.Level > len(pkg) {
			return ""
		}
		pkg = pkg[:len(pkg)-item.Level+1]
		name = strings.Join(pkg, ".")
		if item.Name != "" {
			name += "." + item.Name
		}
	}
	if item.From {
		if item.Member == "*" {
			return ""
		}
		return name + "." + item.Member
	}
	return name
}

// Relations resolves lexical/import references only. It does not infer receiver
// types, dynamic dispatch, re-exports or runtime reachability. An unresolved
// call is retained rather than fabricated, and cycles never become blockers.
func Relations(index Index) []Relation {
	targets := make(map[string]string)
	counts := make(map[string]int)
	classes := make(map[string]bool)
	for _, file := range index.Files {
		if fileLanguage(file) != "python" {
			continue
		}
		counts[file.Module]++
		targets[file.Module] = moduleID(file.Path)
		for _, symbol := range file.Symbols {
			key := file.Module + "." + symbol.Name
			counts[key]++
			targets[key] = symbol.ID
			classes[symbol.ID] = symbol.Kind == "class"
		}
	}
	for key, count := range counts {
		if count != 1 {
			delete(targets, key)
		}
	}
	result := make([]Relation, 0)
	for _, file := range index.Files {
		if fileLanguage(file) != "python" {
			continue
		}
		bindings := make(map[string]map[string]string)
		blocked := make(map[string]map[string]bool)
		for owner, names := range file.Blocked {
			blocked[owner] = make(map[string]bool)
			for _, name := range names {
				blocked[owner][name] = true
			}
		}
		for _, item := range file.Imports {
			name := importedName(file, item)
			target := targets[name]
			resolution := "unresolved"
			if target != "" {
				resolution = "static_reference"
			}
			result = append(result, Relation{Source: item.Owner, Target: target, Kind: "imports", Name: name, Path: file.Path, Line: item.Line, Resolution: resolution})
			if bindings[item.Owner] == nil {
				bindings[item.Owner] = make(map[string]string)
			}
			if !item.From && !item.ExplicitAlias {
				name = strings.Split(name, ".")[0]
			}
			if _, exists := bindings[item.Owner][item.Alias]; exists {
				if blocked[item.Owner] == nil {
					blocked[item.Owner] = make(map[string]bool)
				}
				blocked[item.Owner][item.Alias] = true
			}
			bindings[item.Owner][item.Alias] = name
		}
		for _, call := range file.Calls {
			target := ""
			first := strings.Split(call.Name, ".")[0]
			for scope := call.Owner; scope != ""; scope = parentScope(scope) {
				// Python method bodies do not close over a class namespace.
				if scope != call.Owner && classes[scope] {
					continue
				}
				if blocked[scope][first] {
					break
				}
				if binding, found := bindings[scope][first]; found {
					target = targets[binding+strings.TrimPrefix(call.Name, first)]
					break
				}
				name := strings.SplitN(scope, "::", 2)[1]
				if name != "" {
					name += "."
				}
				if candidate := targets[file.Module+"."+name+first]; candidate != "" {
					target = targets[file.Module+"."+name+call.Name]
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
	return append(result, nativeRelations(index)...)
}

type Query struct {
	Files     []File           `json:"files"`
	Relations []Relation       `json:"relations"`
	Knowledge []knowledge.View `json:"knowledge"`
	Warnings  []string         `json:"warnings,omitempty"`
}

func Select(index Index, state knowledge.State, query string, limit int) Query {
	result := Query{Files: []File{}, Relations: []Relation{}, Knowledge: []knowledge.View{}, Warnings: index.Warnings}
	query = strings.ToLower(strings.TrimSpace(query))
	selected := make(map[string]bool)
	linked := make(map[string]bool)
	if query != "" {
		for _, view := range state.Records {
			if strings.Contains(strings.ToLower(view.ID+" "+view.Issue+" "+view.Module), query) {
				for _, source := range view.Sources {
					linked[source.Path] = true
				}
				for _, symbol := range view.Symbols {
					linked[strings.SplitN(symbol, "::", 2)[0]] = true
				}
			}
		}
	}
	for _, file := range index.Files {
		match := query == "" || linked[file.Path] || strings.Contains(strings.ToLower(file.Path+" "+file.Module), query)
		for _, symbol := range file.Symbols {
			if strings.Contains(strings.ToLower(symbol.ID), query) {
				match = true
			}
		}
		if !match {
			continue
		}
		if len(result.Files) >= limit {
			result.Warnings = append(result.Warnings, fmt.Sprintf("Results limited to %d files; narrow the query", limit))
			break
		}
		result.Files = append(result.Files, file)
		selected[file.Path] = true
	}
	for _, relation := range Relations(index) {
		if selected[relation.Path] {
			result.Relations = append(result.Relations, relation)
		}
	}
	for _, view := range state.Records {
		match := query == "" || strings.Contains(strings.ToLower(view.ID+" "+view.Issue+" "+view.Module), query)
		for _, source := range view.Sources {
			if selected[source.Path] {
				match = true
			}
		}
		if match {
			result.Knowledge = append(result.Knowledge, view)
		}
	}
	return result
}

func Graph(query Query) (graphview.Page, error) {
	page := graphview.Page{Title: "Beads · Índice de código", Knowledge: true, Nodes: []graphview.Node{}, Links: []graphview.Edge{}, Warnings: query.Warnings}
	nodes := make(map[string]graphview.Node)
	for _, file := range query.Files {
		nodes[moduleID(file.Path)] = graphview.Node{ID: moduleID(file.Path), Title: file.Path, Type: "module", Validity: file.Validity, Detail: file}
		counts := make(map[string]int)
		for _, symbol := range file.Symbols {
			counts[symbol.ID]++
		}
		for _, symbol := range file.Symbols {
			if counts[symbol.ID] != 1 {
				page.Warnings = append(page.Warnings, "Ambiguous symbol omitted: "+symbol.ID)
				continue
			}
			nodes[symbol.ID] = graphview.Node{ID: symbol.ID, Title: symbol.Name, Type: symbol.Kind, Layer: 1, Validity: file.Validity, Detail: struct {
				Symbol Symbol `json:"symbol"`
				Path   string `json:"path"`
				SHA256 string `json:"sha256"`
			}{symbol, file.Path, file.SHA256}}
			page.Links = append(page.Links, graphview.Edge{Source: symbol.Parent, Target: symbol.ID, Type: "defines"})
		}
	}
	for _, relation := range query.Relations {
		if _, source := nodes[relation.Source]; !source {
			continue
		}
		if _, target := nodes[relation.Target]; !target {
			continue
		}
		page.Links = append(page.Links, graphview.Edge{Source: relation.Source, Target: relation.Target, Type: relation.Kind})
	}
	for _, view := range query.Knowledge {
		id := "record:" + view.ID
		nodes[id] = graphview.Node{ID: id, Title: view.ID + ": " + view.Summary, Type: view.Kind, Layer: 2, Validity: view.Validity, Detail: view}
		for _, source := range view.Sources {
			if _, exists := nodes[moduleID(source.Path)]; exists {
				page.Links = append(page.Links, graphview.Edge{Source: id, Target: moduleID(source.Path), Type: "supported-by"})
			}
		}
		for _, symbol := range view.Symbols {
			if _, exists := nodes[symbol]; exists {
				page.Links = append(page.Links, graphview.Edge{Source: id, Target: symbol, Type: "learned-at"})
			}
		}
		if view.Issue != "" {
			issue := "issue:" + view.Issue
			nodes[issue] = graphview.Node{ID: issue, Title: view.Issue, Type: "issue", Layer: 3, Detail: "Stored issue reference; inspect with bd show. No blocker inferred."}
			page.Links = append(page.Links, graphview.Edge{Source: id, Target: issue, Type: "recorded-for"})
		}
	}
	if len(nodes) > 3000 {
		return page, errorsTooManyNodes()
	}
	links := make([]graphview.Edge, 0, len(page.Links))
	for _, link := range page.Links {
		if _, ok := nodes[link.Source]; !ok {
			continue
		}
		if _, ok := nodes[link.Target]; !ok {
			continue
		}
		links = append(links, link)
	}
	page.Links = links
	for _, node := range nodes {
		page.Nodes = append(page.Nodes, node)
	}
	sort.Slice(page.Nodes, func(i, j int) bool { return page.Nodes[i].ID < page.Nodes[j].ID })
	return page, nil
}

func errorsTooManyNodes() error {
	return fmt.Errorf("more than 3000 graph nodes; select a module or symbol")
}
