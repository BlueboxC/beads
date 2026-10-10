package main

import (
	"fmt"
	"sort"

	graph "github.com/steveyegge/beads/graphops"
	"github.com/steveyegge/beads/internal/storage/graphstore"
)

const graphGenericBound = 1000
const graphGenericOutputLimit = 1 << 20

type graphGenericInput struct {
	Root                      string
	Direction                 string
	Depth, MaxNodes, MaxLinks int
}

// These allowlists are a private CLI summary, not new BDP record schemas.
type graphGenericNode struct {
	ID          string                 `json:"id"`
	Type        string                 `json:"type"`
	Title       string                 `json:"title"`
	Version     string                 `json:"version"`
	Attribution graphstore.Attribution `json:"attribution"`
}
type graphGenericLink struct {
	ID          string                 `json:"id"`
	Type        string                 `json:"type"`
	Source      string                 `json:"source"`
	Target      string                 `json:"target"`
	Version     string                 `json:"version"`
	Attribution graphstore.Attribution `json:"attribution"`
}
type graphGenericResult struct {
	Projection string             `json:"projection"`
	Scope      string             `json:"scope"`
	Root       string             `json:"root"`
	Direction  string             `json:"direction"`
	Depth      int                `json:"depth"`
	MaxNodes   int                `json:"maxNodes"`
	MaxLinks   int                `json:"maxLinks"`
	Nodes      []graphGenericNode `json:"nodes"`
	Links      []graphGenericLink `json:"links"`
	Frontier   []string           `json:"frontier"`
	Complete   bool               `json:"complete"`
}

func validateGraphGenericBounds(in graphGenericInput) error {
	if in.Direction != "in" && in.Direction != "out" && in.Direction != "both" {
		return fmt.Errorf("direction must be in, out or both")
	}
	if in.Depth < 0 || in.Depth > graphGenericBound || in.MaxNodes < 1 || in.MaxNodes > graphGenericBound || in.MaxLinks < 1 || in.MaxLinks > graphGenericBound {
		return fmt.Errorf("depth must be 0..%d and node/Link bounds 1..%d", graphGenericBound, graphGenericBound)
	}
	return nil
}

// projectGraphGeneric uses exactly the supplied checked snapshot. It never
// follows Owned copies, fetches an endpoint, writes, or infers history ordering.
// #5283 motivates retaining diamond Links independently of node visitation;
// #6148 motivates direction/permutation tests. Their native algorithms remain
// contributor-owned and are neither imported nor replaced here.
func projectGraphGeneric(snapshot graphstore.Snapshot, scope string, in graphGenericInput) (graphGenericResult, error) {
	zero := graphGenericResult{}
	invalid := func(message string) (graphGenericResult, error) {
		return zero, fmt.Errorf("%w: %s", graphstore.ErrInvalidStore, message)
	}
	if err := validateGraphGenericBounds(in); err != nil {
		return zero, fmt.Errorf("%w: %v", graph.ErrValidation, err)
	}
	if err := graph.ValidateScopeURL(scope); err != nil {
		return invalid("invalid snapshot Scope")
	}
	if len(snapshot.Records) > graphstore.PreviewSnapshotLimit {
		return zero, fmt.Errorf("%w: current inventory exceeds %d Resources", graphstore.ErrLimitExceeded, graphstore.PreviewSnapshotLimit)
	}
	nodes := map[string]graphGenericNode{}
	links := map[string]graphGenericLink{}
	for _, value := range snapshot.Records {
		var node graphGenericNode
		switch record := value.(type) {
		case graphstore.Record:
			if record.Type != graphstore.MemoryTypeURL(scope) {
				return invalid("unsupported Memory Type")
			}
			node = graphGenericNode{record.ID, record.Type, record.Properties.Title, record.Version, record.Attribution}
		case graphstore.IssueRecord:
			if record.Properties == nil || record.Type != graphstore.IssueTypeURL(scope) {
				return invalid("unsupported or incomplete Issue")
			}
			node = graphGenericNode{record.ID, record.Type, record.Properties.Title, record.Version, record.Attribution}
		case graphstore.LinkRecord:
			if !graphstore.IsInformationalTypeURL(scope, record.Type) && record.Type != graphstore.DependencyTypeURL(scope) {
				return invalid("unsupported Link Type")
			}
			if _, kind, ok := graph.SplitCanonicalURL(scope, record.ID); !ok || kind != graph.KindLink || record.Version == "" {
				return invalid("invalid Link identity/version")
			}
			if _, duplicate := links[record.ID]; duplicate {
				return invalid("duplicate Link identity")
			}
			links[record.ID] = graphGenericLink{record.ID, record.Type, record.Source, record.Target, record.Version, record.Attribution}
			continue
		default:
			return invalid("unsupported snapshot record kind")
		}
		if _, kind, ok := graph.SplitCanonicalURL(scope, node.ID); !ok || kind != graph.KindBead || node.Version == "" {
			return invalid("invalid Bead identity/version")
		}
		if _, duplicate := nodes[node.ID]; duplicate {
			return invalid("duplicate Bead identity")
		}
		nodes[node.ID] = node
	}
	adjacency := map[string][]graphGenericLink{}
	for _, link := range links {
		if _, ok := nodes[link.Source]; !ok {
			return invalid("unsupported or missing Link source")
		}
		if _, ok := nodes[link.Target]; !ok {
			return invalid("unsupported or missing Link target")
		}
		if in.Direction != "in" {
			adjacency[link.Source] = append(adjacency[link.Source], link)
		}
		if in.Direction != "out" && (in.Direction == "in" || link.Target != link.Source) {
			adjacency[link.Target] = append(adjacency[link.Target], link)
		}
	}
	for id := range adjacency {
		sort.Slice(adjacency[id], func(i, j int) bool { return graph.CompareCodeUnits(adjacency[id][i].ID, adjacency[id][j].ID) < 0 })
	}
	if _, ok := nodes[in.Root]; !ok {
		return zero, graphstore.ErrNotFound
	}
	distance := map[string]int{in.Root: 0}
	queue := []string{in.Root}
	emitted := map[string]graphGenericLink{}
	for cursor := 0; cursor < len(queue); cursor++ {
		id := queue[cursor]
		if distance[id] >= in.Depth {
			continue
		}
		for _, link := range adjacency[id] {
			next := link.Target
			if in.Direction == "in" || (in.Direction == "both" && id != link.Source) {
				next = link.Source
			}
			if _, seen := emitted[link.ID]; !seen {
				if len(emitted) >= in.MaxLinks {
					return zero, fmt.Errorf("%w: traversal exceeds %d Links", graphstore.ErrLimitExceeded, in.MaxLinks)
				}
				emitted[link.ID] = link
			}
			if _, seen := distance[next]; !seen {
				if len(distance) >= in.MaxNodes {
					return zero, fmt.Errorf("%w: traversal exceeds %d Beads", graphstore.ErrLimitExceeded, in.MaxNodes)
				}
				distance[next] = distance[id] + 1
				queue = append(queue, next)
			}
		}
	}
	result := graphGenericResult{Projection: "summary", Scope: scope, Root: in.Root, Direction: in.Direction, Depth: in.Depth, MaxNodes: in.MaxNodes, MaxLinks: in.MaxLinks, Nodes: []graphGenericNode{}, Links: []graphGenericLink{}, Frontier: []string{}}
	for id := range distance {
		result.Nodes = append(result.Nodes, nodes[id])
		// Frontier is about missing eligible Link identities, not just missing
		// neighbors. A boundary diamond/self edge can remain even if all of its
		// endpoints were reached. Inspection alone does not consume output caps.
		for _, link := range adjacency[id] {
			if _, seen := emitted[link.ID]; !seen {
				result.Frontier = append(result.Frontier, id)
				break
			}
		}
	}
	for _, link := range emitted {
		result.Links = append(result.Links, link)
	}
	sort.Slice(result.Nodes, func(i, j int) bool { return graph.CompareCodeUnits(result.Nodes[i].ID, result.Nodes[j].ID) < 0 })
	sort.Slice(result.Links, func(i, j int) bool { return graph.CompareCodeUnits(result.Links[i].ID, result.Links[j].ID) < 0 })
	sort.Slice(result.Frontier, func(i, j int) bool { return graph.CompareCodeUnits(result.Frontier[i], result.Frontier[j]) < 0 })
	result.Complete = len(result.Frontier) == 0
	return result, nil
}
