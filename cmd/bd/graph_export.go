package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/steveyegge/beads/internal/graphview"
	"github.com/steveyegge/beads/internal/types"
)

// renderGraphDOT renders the graph in Graphviz DOT format.
// Output can be piped to graphviz: bd graph --dot <id> | dot -Tsvg > graph.svg
func renderGraphDOT(out io.Writer, layout *GraphLayout, subgraph *TemplateSubgraph) error {
	w := &graphExportWriter{out: out}
	if len(layout.Nodes) == 0 {
		w.println("digraph beads { }")
		return w.wrapError("DOT")
	}

	w.println("digraph beads {")
	w.println("  rankdir=LR;")
	w.println("  node [shape=box, style=\"rounded,filled\", fontname=\"Helvetica\", fontsize=11];")
	w.println("  edge [color=\"#666666\"];")
	w.println()

	// Emit nodes grouped by layer using subgraph clusters for rank alignment
	for layerIdx, layer := range layout.Layers {
		w.printf("  subgraph cluster_layer_%d {\n", layerIdx)
		w.println("    style=invis;")
		w.printf("    rank=same;\n")
		for _, id := range layer {
			node := layout.Nodes[id]
			if node == nil {
				continue
			}
			label, fillColor, fontColor := dotNodeAttrs(node)
			// Escape quotes in label
			label = strings.ReplaceAll(label, "\"", "\\\"")
			w.printf("    \"%s\" [label=\"%s\", fillcolor=\"%s\", fontcolor=\"%s\"];\n",
				dotEscapeID(id), label, fillColor, fontColor)
		}
		w.println("  }")
	}
	w.println()

	// Emit edges
	for _, dep := range subgraph.Dependencies {
		// Only include blocking dependencies in the graph
		if dep.Type != types.DepBlocks && dep.Type != types.DepParentChild {
			continue
		}
		// Ensure both endpoints exist in the subgraph
		if layout.Nodes[dep.IssueID] == nil || layout.Nodes[dep.DependsOnID] == nil {
			continue
		}
		edgeStyle := dotEdgeStyle(dep.Type)
		// dep.DependsOnID -> dep.IssueID (blocker points to blocked)
		w.printf("  \"%s\" -> \"%s\"%s;\n",
			dotEscapeID(dep.DependsOnID), dotEscapeID(dep.IssueID), edgeStyle)
	}

	w.println("}")
	return w.wrapError("DOT")
}

// graphExportWriter records the first output failure and suppresses later
// writes. Writer-aware graph and list paths can therefore return one stable
// root cause without buffering their whole output or changing successful bytes.
type graphExportWriter struct {
	out io.Writer
	err error
}

func (w *graphExportWriter) printf(format string, args ...interface{}) {
	if w.err != nil {
		return
	}
	_, w.err = fmt.Fprintf(w.out, format, args...)
}

func (w *graphExportWriter) println(args ...interface{}) {
	if w.err != nil {
		return
	}
	_, w.err = fmt.Fprintln(w.out, args...)
}

func (w *graphExportWriter) wrapError(kind string) error {
	if w.err == nil {
		return nil
	}
	return fmt.Errorf("writing %s output: %w", kind, w.err)
}

// dotNodeAttrs returns the DOT label, fill color, and font color for a node
func dotNodeAttrs(node *GraphNode) (label, fillColor, fontColor string) {
	icon := statusPlainIcon(node.Issue.Status)
	title := truncateTitle(node.Issue.Title, 40)
	label = fmt.Sprintf("%s %s\\nP%d | %s", icon, node.Issue.ID, node.Issue.Priority, title)

	switch node.Issue.Status {
	case types.StatusOpen:
		fillColor = "#e8f4fd"
		fontColor = "#1a1a1a"
	case types.StatusInProgress:
		fillColor = "#fff3cd"
		fontColor = "#664d03"
	case types.StatusBlocked:
		fillColor = "#f8d7da"
		fontColor = "#842029"
	case types.StatusClosed:
		fillColor = "#d4edda"
		fontColor = "#888888"
	default: // deferred, hooked, etc.
		fillColor = "#e2e3e5"
		fontColor = "#41464b"
	}
	return
}

// dotEdgeStyle returns DOT edge attributes for a dependency type
func dotEdgeStyle(depType types.DependencyType) string {
	switch depType {
	case types.DepBlocks:
		return " [style=solid, arrowhead=normal]"
	case types.DepParentChild:
		return " [style=dashed, arrowhead=empty, color=\"#999999\"]"
	default:
		return ""
	}
}

// dotEscapeID escapes an ID for DOT format by replacing characters
// that could break quoted strings (backslash, double-quote).
func dotEscapeID(id string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return r.Replace(id)
}

// statusPlainIcon returns a plain text status icon (no ANSI colors) for export formats
func statusPlainIcon(status types.Status) string {
	switch status {
	case types.StatusOpen:
		return "○"
	case types.StatusInProgress:
		return "◐"
	case types.StatusBlocked:
		return "●"
	case types.StatusClosed:
		return "✓"
	default:
		return "❄"
	}
}

// renderGraphHTML generates a self-contained HTML file with an interactive D3.js
// force-directed graph visualization. The output is a complete HTML document that
// can be opened in any browser.
func renderGraphHTML(out io.Writer, layout *GraphLayout, subgraph *TemplateSubgraph) error {
	nodes := buildHTMLGraphData(layout, subgraph)
	edges := buildHTMLEdgeData(layout, subgraph)

	title := "Beads Dependency Graph"
	if subgraph.Root != nil {
		title = fmt.Sprintf("Beads: %s (%s)", subgraph.Root.Title, subgraph.Root.ID)
	}

	return graphview.WriteHTML(out, graphview.Page{Title: title, Nodes: nodes, Links: edges})
}

// HTMLNode is the JSON structure for a node in the HTML visualization
type HTMLNode = graphview.Node

// HTMLEdge is the JSON structure for an edge in the HTML visualization.
type HTMLEdge = graphview.Edge

func buildHTMLGraphData(layout *GraphLayout, _ *TemplateSubgraph) []HTMLNode {
	nodes := make([]HTMLNode, 0, len(layout.Nodes))
	for _, node := range layout.Nodes {
		nodes = append(nodes, HTMLNode{
			ID:       node.Issue.ID,
			Title:    node.Issue.Title,
			Status:   string(node.Issue.Status),
			Priority: node.Issue.Priority,
			Type:     string(node.Issue.IssueType),
			Layer:    node.Layer,
			Assignee: node.Issue.Assignee,
			Detail:   node.Issue,
		})
	}
	return nodes
}

func buildHTMLEdgeData(layout *GraphLayout, subgraph *TemplateSubgraph) []HTMLEdge {
	edges := make([]HTMLEdge, 0, len(subgraph.Dependencies))
	for _, dep := range subgraph.Dependencies {
		if dep.Type != types.DepBlocks && dep.Type != types.DepParentChild {
			continue
		}
		if layout.Nodes[dep.IssueID] == nil || layout.Nodes[dep.DependsOnID] == nil {
			continue
		}
		edges = append(edges, HTMLEdge{
			Source: dep.DependsOnID,
			Target: dep.IssueID,
			Type:   string(dep.Type),
		})
	}
	return edges
}
