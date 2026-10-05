package knowledge

import (
	"github.com/steveyegge/beads/internal/graphview"
	"io"
	"sort"
)

type Node struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Kind     string `json:"kind"`
	Validity string `json:"validity,omitempty"`
	Status   string `json:"status,omitempty"`
	Detail   any    `json:"detail"`
}

type Edge struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Kind   string `json:"kind"`
}

type Graph struct {
	Nodes    []Node   `json:"nodes"`
	Edges    []Edge   `json:"edges"`
	Warnings []string `json:"warnings,omitempty"`
}

// BuildGraph reads records and provenance, never task blocker semantics.
func BuildGraph(state State) Graph {
	graph := Graph{Nodes: []Node{}, Edges: []Edge{}, Warnings: state.Warnings}
	nodes := make(map[string]Node)
	edges := make(map[Edge]bool)
	addSource := func(source Source) {
		id := "source:" + source.Path
		nodes[id] = Node{ID: id, Label: source.Path, Kind: "source", Detail: source}
		for _, contract := range source.Contracts {
			edges[Edge{Source: id, Target: "source:" + contract, Kind: "governed-by"}] = true
		}
	}
	for _, source := range state.Catalog.Sources {
		addSource(source)
	}
	for _, view := range state.Records {
		id := "record:" + view.ID
		nodes[id] = Node{ID: id, Label: view.ID + ": " + view.Summary, Kind: view.Kind, Validity: view.Validity, Detail: view}
		for _, source := range view.Sources {
			addSource(source)
			edges[Edge{Source: id, Target: "source:" + source.Path, Kind: "supported-by"}] = true
		}
		if view.Issue != "" {
			issueID := "issue:" + view.Issue
			nodes[issueID] = Node{ID: issueID, Label: view.Issue, Kind: "issue", Detail: "Issue reference; inspect with bd show. Closing it does not verify the record."}
			edges[Edge{Source: id, Target: issueID, Kind: "recorded-for"}] = true
		}
		for _, related := range view.Related {
			edges[Edge{Source: id, Target: "record:" + related, Kind: "related"}] = true
		}
	}
	for _, p := range state.Proposals {
		id := "proposal:" + p.ID
		nodes[id] = Node{ID: id, Label: p.Record.ID + ": " + p.Summary, Kind: "proposal", Status: p.Status, Validity: p.Validity, Detail: p}
		for _, source := range p.Sources {
			addSource(source)
			edges[Edge{Source: id, Target: "source:" + source.Path, Kind: "proposed-from"}] = true
		}
		if p.Issue != "" {
			issueID := "issue:" + p.Issue
			if _, ok := nodes[issueID]; !ok {
				nodes[issueID] = Node{ID: issueID, Label: p.Issue, Kind: "issue", Detail: "Proposal task reference, never verification"}
			}
			edges[Edge{Source: id, Target: issueID, Kind: "recorded-for"}] = true
		}
		for _, related := range p.Related {
			edges[Edge{Source: id, Target: "record:" + related, Kind: "related"}] = true
		}
		if p.Supersedes != "" {
			edges[Edge{Source: id, Target: "proposal:" + p.Supersedes, Kind: "supersedes"}] = true
		}
		if p.Status == "accepted" {
			edges[Edge{Source: id, Target: "record:" + p.Record.ID, Kind: "reviewed-as"}] = true
		}
	}
	for edge := range edges {
		if _, ok := nodes[edge.Source]; !ok {
			continue
		}
		if _, ok := nodes[edge.Target]; !ok {
			continue
		}
		graph.Edges = append(graph.Edges, edge)
	}
	for _, node := range nodes {
		graph.Nodes = append(graph.Nodes, node)
	}
	sort.Slice(graph.Nodes, func(i, j int) bool { return graph.Nodes[i].ID < graph.Nodes[j].ID })
	sort.Slice(graph.Edges, func(i, j int) bool {
		a, b := graph.Edges[i], graph.Edges[j]
		return a.Source+"\x00"+a.Target+"\x00"+a.Kind < b.Source+"\x00"+b.Target+"\x00"+b.Kind
	})
	return graph
}

// WriteHTML projects provenance into the same native viewer used by bd graph.
// Node colors describe knowledge types, never task completion or qualification.
func WriteHTML(out io.Writer, graph Graph) error {
	page := graphview.Page{Title: "Beads · Conocimiento del proyecto", Knowledge: true, Nodes: make([]graphview.Node, 0, len(graph.Nodes)), Links: make([]graphview.Edge, 0, len(graph.Edges)), Warnings: graph.Warnings}
	for _, node := range graph.Nodes {
		layer := 1
		if node.Kind == "source" {
			layer = 0
		} else if node.Kind == "issue" {
			layer = 2
		}
		page.Nodes = append(page.Nodes, graphview.Node{ID: node.ID, Title: node.Label, Type: node.Kind, Status: node.Status, Layer: layer, Validity: node.Validity, Detail: node.Detail})
	}
	for _, edge := range graph.Edges {
		page.Links = append(page.Links, graphview.Edge{Source: edge.Source, Target: edge.Target, Type: edge.Kind})
	}
	return graphview.WriteHTML(out, page)
}
