package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/activity"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/codeindex"
	"github.com/steveyegge/beads/internal/graphview"
	"github.com/steveyegge/beads/internal/knowledge"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
	"github.com/steveyegge/beads/memoryops"
)

func runProjectGraph(cmd *cobra.Command, args []string) error {
	if len(args) != 0 || graphAll || graphOpen || graphCompact || graphBox || graphDOT {
		return errors.New("--project selects a workspace overview; do not combine with issue IDs or task-only display flags")
	}
	if graphHTML && jsonOutput {
		return errors.New("choose --html or --json")
	}
	if usesProxiedServer() {
		return errors.New("--project currently requires a direct workspace connection")
	}
	memories, reader, err := openKnowledge()
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()
	plane, err := memories.List(rootCtx, memoryops.ListRequest{})
	if err != nil {
		return err
	}
	state := reader.Refresh(knowledge.Decode(plane.Memories))
	index, err := codeindex.Load(plane.Memories)
	if err != nil {
		return err
	}
	workspace := filepath.Dir(beads.FindBeadsDir())
	if index.Version != 0 {
		codeReader, err := codeindex.Open(workspace)
		if err != nil {
			return err
		}
		defer func() { _ = codeReader.Close() }()
		index = codeReader.Refresh(index)
	}
	maxRows, source, err := resolveMaxRows(cmd)
	if err != nil {
		return err
	}
	unlimited := 0
	filter, err := workapi.BuildListFilter(issueops.ListRequest{Status: "all", AllFlag: true, IncludeAllTypes: true, Limit: &unlimited, MaxRows: maxRows, MaxRowsSource: source}, workapi.ListConfig{})
	if err != nil {
		return err
	}
	issues, err := store.SearchIssues(rootCtx, "", filter)
	if err != nil {
		if capErr := handleMaxRowsError(err); capErr != nil {
			return capErr
		}
		return err
	}
	var deps []*types.Dependency
	for _, issue := range issues {
		records, err := store.GetDependencyRecords(rootCtx, issue.ID)
		if err != nil {
			return fmt.Errorf("reading dependencies for %s: %w", issue.ID, err)
		}
		deps = append(deps, records...)
	}
	page := buildProjectGraph(workspace, state, index, issues, deps)
	journal := activity.Read(plane.Memories, "", "", 1000)
	page.Activity = &journal
	if graphHTML {
		return graphview.WriteHTML(cmd.OutOrStdout(), page)
	}
	return outputJSON(page)
}

// buildProjectGraph composes snapshots only. Directory nodes summarize the
// complete index; files and symbol locations remain available in the viewer.
// Resolved syntax edges are aggregated, never promoted to task dependencies.
func buildProjectGraph(workspace string, state knowledge.State, index codeindex.Index, issues []*types.Issue, deps []*types.Dependency) graphview.Page {
	page := graphview.Page{Title: "Beads · " + filepath.Base(workspace), Knowledge: true, Workspace: workspace,
		Nodes: []graphview.Node{}, Links: []graphview.Edge{}, Files: []graphview.EvidenceFile{},
		Warnings: append(append([]string{}, state.Warnings...), index.Warnings...)}
	nodes := map[string]graphview.Node{}
	graph := knowledge.BuildGraph(state)
	for _, n := range graph.Nodes {
		layer := 1
		if n.Kind == "source" {
			layer = 0
		}
		nodes[n.ID] = graphview.Node{ID: n.ID, Title: n.Label, Type: n.Kind, Status: n.Status, Layer: layer, Validity: n.Validity, Detail: n.Detail}
	}
	type edgeKey struct{ source, target, kind string }
	edges := map[edgeKey]graphview.Edge{}
	add := func(source, target, kind string, count int, validity string) {
		key := edgeKey{source, target, kind}
		edge := edges[key]
		edge.Source, edge.Target, edge.Type = source, target, kind
		edge.Count += count
		if validity == "needs_review" {
			edge.Validity = validity
		}
		edges[key] = edge
	}
	for _, e := range graph.Edges {
		add(e.Source, e.Target, e.Kind, 0, "")
	}
	for _, issue := range issues {
		id := "issue:" + issue.ID
		nodes[id] = graphview.Node{ID: id, Title: issue.Title, Type: "issue", Status: string(issue.Status), Priority: issue.Priority, Assignee: issue.Assignee, Layer: 2, Detail: issue}
	}
	for _, dep := range deps {
		add("issue:"+dep.DependsOnID, "issue:"+dep.IssueID, string(dep.Type), 0, "")
	}
	type directory struct {
		Directory  string   `json:"directory"`
		Files      []string `json:"files"`
		Symbols    int      `json:"symbol_count"`
		Unresolved int      `json:"unresolved_references"`
		Validity   string   `json:"validity"`
	}
	dirs := map[string]*directory{}
	files := map[string]codeindex.File{}
	for _, file := range index.Files {
		dir := filepath.ToSlash(filepath.Dir(file.Path))
		if dirs[dir] == nil {
			dirs[dir] = &directory{Directory: dir, Files: []string{}, Validity: "current"}
		}
		d := dirs[dir]
		d.Files = append(d.Files, file.Path)
		d.Symbols += len(file.Symbols)
		if file.Validity != "current" || file.ParseError != "" {
			d.Validity = "needs_review"
		}
		files[file.Path] = file
		entry := graphview.EvidenceFile{Path: file.Path, Language: file.Language, SHA256: file.SHA256, Validity: file.Validity, ParseError: file.ParseError}
		for _, s := range file.Symbols {
			entry.Symbols = append(entry.Symbols, graphview.SourceSymbol{ID: s.ID, Name: s.Name, Kind: s.Kind, Line: s.Line, EndLine: s.EndLine})
		}
		for _, contract := range file.Contracts {
			entry.Contracts = append(entry.Contracts, contract.Path)
		}
		page.Files = append(page.Files, entry)
	}
	for _, ref := range codeindex.Relations(index) {
		from := filepath.ToSlash(filepath.Dir(ref.Path))
		toPath := strings.SplitN(ref.Target, "::", 2)[0]
		to := filepath.ToSlash(filepath.Dir(toPath))
		if ref.Resolution != "static_reference" || ref.Target == "" {
			if dirs[from] != nil {
				dirs[from].Unresolved++
			}
			continue
		}
		if dirs[from] == nil || dirs[to] == nil || from == to {
			continue
		}
		validity := ""
		if files[ref.Path].Validity != "current" || files[toPath].Validity != "current" {
			validity = "needs_review"
		}
		add("directory:"+from, "directory:"+to, ref.Kind, 1, validity)
	}
	for dir, d := range dirs {
		sort.Strings(d.Files)
		id := "directory:" + dir
		nodes[id] = graphview.Node{ID: id, Title: dir, Type: "code_module", Validity: d.Validity, Layer: 0, Detail: d}
	}
	for _, source := range graph.Nodes {
		if source.Kind != "source" {
			continue
		}
		path := strings.TrimPrefix(source.ID, "source:")
		dir := filepath.ToSlash(filepath.Dir(path))
		if dirs[dir] != nil {
			add("directory:"+dir, source.ID, "contains", 0, "")
		}
	}
	for _, view := range state.Records {
		for _, symbol := range view.Symbols {
			path := strings.SplitN(symbol, "::", 2)[0]
			dir := filepath.ToSlash(filepath.Dir(path))
			if dirs[dir] != nil {
				add("record:"+view.ID, "directory:"+dir, "learned-at", 0, view.Validity)
			}
		}
	}
	for _, p := range state.Proposals {
		for _, symbol := range p.Symbols {
			dir := filepath.ToSlash(filepath.Dir(strings.SplitN(symbol, "::", 2)[0]))
			if dirs[dir] != nil {
				add("proposal:"+p.ID, "directory:"+dir, "proposed-at", 0, p.Validity)
			}
		}
	}
	for _, node := range nodes {
		page.Nodes = append(page.Nodes, node)
	}
	for _, edge := range edges {
		if _, ok := nodes[edge.Source]; !ok {
			continue
		}
		if _, ok := nodes[edge.Target]; !ok {
			continue
		}
		page.Links = append(page.Links, edge)
	}
	sort.Slice(page.Nodes, func(i, j int) bool { return page.Nodes[i].ID < page.Nodes[j].ID })
	sort.Slice(page.Links, func(i, j int) bool {
		a, b := page.Links[i], page.Links[j]
		return a.Source+"\x00"+a.Target+"\x00"+a.Type < b.Source+"\x00"+b.Target+"\x00"+b.Type
	})
	sort.Slice(page.Files, func(i, j int) bool { return page.Files[i].Path < page.Files[j].Path })
	return page
}
