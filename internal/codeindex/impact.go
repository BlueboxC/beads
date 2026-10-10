package codeindex

import (
	"errors"
	"path"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/knowledge"
)

type ImpactFile struct {
	Path     string     `json:"path"`
	Depth    int        `json:"depth"`
	Validity string     `json:"validity"`
	Via      []Relation `json:"via"`
}

type ImpactTest struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type ImpactReport struct {
	Selector        string           `json:"selector"`
	Granularity     string           `json:"granularity"`
	Files           []ImpactFile     `json:"files"`
	Tests           []ImpactTest     `json:"candidate_tests"`
	Knowledge       []knowledge.View `json:"knowledge"`
	Issues          []string         `json:"issue_references"`
	Unresolved      []Relation       `json:"unresolved"`
	UnresolvedTotal int              `json:"unresolved_total"`
	Omitted         int              `json:"omitted_files"`
	DepthLimited    bool             `json:"depth_limited"`
	Warnings        []string         `json:"warnings"`
}

func testPath(p string) bool {
	b := path.Base(p)
	return strings.HasPrefix(p, "tests/") || strings.Contains(p, "/tests/") || strings.HasPrefix(b, "test_") || strings.HasSuffix(b, "_test.go") || strings.Contains(b, ".test.") || strings.Contains(b, ".spec.")
}

// Impact walks inverse file dependencies, conservatively including imports and
// calls. Witness paths explain each candidate; neither test coverage nor runtime
// reachability is inferred. Even a symbol selector uses file granularity.
func Impact(index Index, state knowledge.State, selector string, maxDepth, limit int) (ImpactReport, error) {
	report := ImpactReport{Selector: selector, Granularity: "conservative_static_file_dependencies", Files: []ImpactFile{}, Tests: []ImpactTest{}, Knowledge: []knowledge.View{}, Issues: []string{}, Unresolved: []Relation{}, Warnings: append([]string(nil), index.Warnings...)}
	if maxDepth < 1 || maxDepth > 32 || limit < 1 || limit > 1024 || selector == "" {
		return report, errors.New("impact requires a path or exact symbol, depth 1-32 and limit 1-1024")
	}
	files := make(map[string]File)
	depth := make(map[string]int)
	via := make(map[string][]Relation)
	queue := []string{}
	for _, file := range index.Files {
		files[file.Path] = file
		match := file.Path == selector || strings.HasPrefix(file.Path, strings.TrimSuffix(selector, "/")+"/")
		for _, symbol := range file.Symbols {
			match = match || symbol.ID == selector
		}
		if match {
			depth[file.Path] = 0
			queue = append(queue, file.Path)
		}
	}
	sort.Strings(queue)
	if len(queue) == 0 || len(queue) > limit {
		return report, errors.New("selector matches no indexed files or exceeds limit; narrow it")
	}
	relations := Relations(index)
	inverse := make(map[string][]Relation)
	for _, ref := range relations {
		if ref.Resolution == "static_reference" {
			target := strings.SplitN(ref.Target, "::", 2)[0]
			if _, ok := files[target]; ok && target != ref.Path {
				inverse[target] = append(inverse[target], ref)
			}
		} else {
			report.UnresolvedTotal++
		}
	}
	for head := 0; head < len(queue); head++ {
		p := queue[head]
		for _, ref := range inverse[p] {
			if _, seen := depth[ref.Path]; seen {
				continue
			}
			if depth[p] == maxDepth {
				report.DepthLimited = true
				continue
			}
			depth[ref.Path] = depth[p] + 1
			via[ref.Path] = append(append([]Relation(nil), via[p]...), ref)
			queue = append(queue, ref.Path)
		}
	}
	// Traverse before limiting presentation, so a display bound never changes
	// the dependency calculation or loses counts of omitted candidates.
	report.Omitted = max(0, len(queue)-limit)
	reached := make(map[string]bool)
	tests := make(map[string]string)
	for _, p := range queue {
		reached[p] = true
		if testPath(p) {
			tests[p] = "static_dependency_candidate"
		}
	}
	for _, p := range queue[:min(limit, len(queue))] {
		report.Files = append(report.Files, ImpactFile{Path: p, Depth: depth[p], Validity: files[p].Validity, Via: via[p]})
	}
	issues := make(map[string]bool)
	for _, view := range state.Records {
		linked := false
		for _, source := range view.Sources {
			linked = linked || reached[source.Path]
		}
		for _, symbol := range view.Symbols {
			linked = linked || reached[strings.SplitN(symbol, "::", 2)[0]]
		}
		if !linked {
			continue
		}
		report.Knowledge = append(report.Knowledge, view)
		if view.Issue != "" {
			issues[view.Issue] = true
		}
		for _, source := range view.Sources {
			if testPath(source.Path) {
				tests[source.Path] = "explicit_learning_evidence_candidate"
			}
		}
	}
	for _, ref := range relations {
		if reached[ref.Path] && ref.Resolution != "static_reference" && len(report.Unresolved) < 1024 {
			report.Unresolved = append(report.Unresolved, ref)
		}
	}
	for p, reason := range tests {
		report.Tests = append(report.Tests, ImpactTest{Path: p, Reason: reason})
	}
	sort.Slice(report.Tests, func(i, j int) bool { return report.Tests[i].Path < report.Tests[j].Path })
	for id := range issues {
		report.Issues = append(report.Issues, id)
	}
	sort.Strings(report.Issues)
	report.Warnings = append(report.Warnings, "Static candidates only: dynamic/external references and unindexed code can add effects; candidate tests are not verified coverage.")
	return report, nil
}
