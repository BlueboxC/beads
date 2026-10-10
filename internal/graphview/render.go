// Package graphview renders the shared native Beads graph viewer.
package graphview

import (
	"embed"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"strings"

	"github.com/steveyegge/beads/internal/activity"
)

// Node preserves the native task graph shape, with optional knowledge details.
type Node struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Priority int    `json:"priority"`
	Type     string `json:"type"`
	Layer    int    `json:"layer"`
	Assignee string `json:"assignee,omitempty"`
	Validity string `json:"validity,omitempty"`
	Detail   any    `json:"detail,omitempty"`
}

// Edge carries a typed relationship; knowledge edges are not task blockers.
type Edge struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	Type     string `json:"type"`
	Count    int    `json:"count,omitempty"`
	Detail   any    `json:"detail,omitempty"`
	Validity string `json:"validity,omitempty"`
}

// SourceSymbol identifies code without embedding or executing source bodies.
type SourceSymbol struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Line    int    `json:"line"`
	EndLine int    `json:"end_line,omitempty"`
}

// EvidenceFile carries locations and provenance for overview drilldown.
type EvidenceFile struct {
	Language   string         `json:"language,omitempty"`
	Path       string         `json:"path"`
	SHA256     string         `json:"sha256"`
	Validity   string         `json:"validity"`
	Symbols    []SourceSymbol `json:"symbols,omitempty"`
	Contracts  []string       `json:"contracts,omitempty"`
	ParseError string         `json:"parse_error,omitempty"`
}

// LiveConfig opts a served page into same-origin bounded polling.
type LiveConfig struct {
	Endpoint string `json:"endpoint"`
	PollMS   int    `json:"poll_ms"`
}

// Page is a snapshot. Live is present only on the served bootstrap page.
type Page struct {
	Activity  *activity.View `json:"activity,omitempty"`
	Live      *LiveConfig    `json:"live,omitempty"`
	Title     string         `json:"title"`
	Nodes     []Node         `json:"nodes"`
	Links     []Edge         `json:"links"`
	Knowledge bool           `json:"knowledge,omitempty"`
	Warnings  []string       `json:"warnings,omitempty"`
	Workspace string         `json:"workspace,omitempty"`
	Files     []EvidenceFile `json:"files,omitempty"`
}

//go:embed viewer.html d3.v7.min.js D3-LICENSE
var assets embed.FS

// WriteHTML uses the native Beads D3 viewer for tasks and knowledge. D3 and its
// license are embedded; stored strings are JSON escaped and inserted as text.
func WriteHTML(out io.Writer, page Page) error {
	nodes, err := json.Marshal(page.Nodes)
	if err != nil {
		return fmt.Errorf("marshaling HTML graph nodes: %w", err)
	}
	links, err := json.Marshal(page.Links)
	if err != nil {
		return fmt.Errorf("marshaling HTML graph edges: %w", err)
	}
	metadata := page
	metadata.Nodes, metadata.Links = nil, nil
	data, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("marshaling HTML graph: %w", err)
	}
	template, err := assets.ReadFile("viewer.html")
	if err != nil {
		return err
	}
	library, err := assets.ReadFile("d3.v7.min.js")
	if err != nil {
		return err
	}
	license, err := assets.ReadFile("D3-LICENSE")
	if err != nil {
		return err
	}
	content := strings.NewReplacer("__TITLE__", html.EscapeString(page.Title), "__D3__", "/*\n"+string(license)+"*/\n"+string(library), "__DATA__", string(data), "__NODES__", string(nodes), "__LINKS__", string(links)).Replace(string(template))
	if _, err = io.WriteString(out, content); err != nil {
		return fmt.Errorf("writing HTML output: %w", err)
	}
	return nil
}
