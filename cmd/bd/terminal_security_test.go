package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

func TestIssueTerminalRenderingSanitizesContent(t *testing.T) {
	issue := &types.Issue{ID: "bd-safe", Title: "Título\x1b[2J seguro\x1b]52;c;dGVzdA==\x07", Assignee: "Ana\x1b]0;spoof\x1b\\", Description: "Body\x1b[31m text", Metadata: json.RawMessage(`{"x":"Título\u001b[2J seguro"}`), Status: types.StatusOpen, Priority: 1, IssueType: types.TypeBug}
	for name, render := range map[string]func() string{
		"list pretty":     func() string { return formatPrettyIssue(issue) },
		"show short":      func() string { return formatShortIssue(issue) },
		"show header":     func() string { return formatIssueHeader(issue) },
		"show metadata":   func() string { return formatIssueMetadata(issue) },
		"custom metadata": func() string { return formatIssueCustomMetadata(issue) },
		"list long":       func() string { var b strings.Builder; formatIssueLong(&b, issue, nil, false); return b.String() },
		"agent list":      func() string { var b strings.Builder; formatAgentIssue(&b, issue, nil, nil, ""); return b.String() },
		"stale":           func() string { return captureADOStdout(t, func() { displayStaleIssues([]*types.Issue{issue}, 30) }) },
		"search long": func() string {
			return captureADOStdout(t, func() { outputSearchResults([]*types.Issue{issue}, "safe", true) })
		},
		"query compact": func() string { var b strings.Builder; formatQueryIssue(&b, issue); return b.String() },
		"dependency":    func() string { return formatSimpleDependencyLine("→", issue) },
		"pour preview": func() string {
			return captureADOStdout(t, func() {
				renderPourDryRun(issue.ID, &TemplateSubgraph{Root: issue, Issues: []*types.Issue{issue}}, nil, issue.Assignee, "blocks", []pourAttachPreview{{title: issue.Title, steps: 1}})
			})
		},
		"wisp preview": func() string {
			return captureADOStdout(t, func() {
				renderWispCreateDryRun(issue.ID, &TemplateSubgraph{Root: issue, Issues: []*types.Issue{issue}}, nil, false)
			})
		},
		"dependency tree": func() string {
			return formatTreeNode(&types.TreeNode{Issue: *issue}, false)
		},
		"dependency annotations": func() string {
			return captureADOStdout(t, func() {
				(&depRender{mode: "all", inView: map[string]*types.Issue{issue.ID: issue}, allDeps: map[string][]*types.Dependency{"bd-root": {{IssueID: "bd-root", DependsOnID: issue.ID, Type: types.DepBlocks}}}}).annotationsFor("bd-root", "")
			})
		},
		"swarm status": func() string {
			return captureADOStdout(t, func() {
				renderSwarmStatus(&SwarmStatus{EpicTitle: issue.Title, Active: []StatusIssue{{ID: issue.ID, Assignee: issue.Assignee}}})
			})
		},
		"swarm analysis": func() string {
			return captureADOStdout(t, func() {
				renderSwarmAnalysis(&SwarmAnalysis{EpicTitle: issue.Title, TotalIssues: 1, ReadyFronts: []ReadyFront{{Issues: []string{issue.ID}, Titles: []string{issue.Title}}}})
			})
		},
		"molecule current": func() string {
			return captureADOStdout(t, func() {
				printMoleculeProgress(&MoleculeProgress{MoleculeID: issue.ID, MoleculeTitle: issue.Title, Assignee: issue.Assignee})
			})
		},
		"molecule progress": func() string {
			return captureADOStdout(t, func() {
				printMoleculeProgressStats(&types.MoleculeProgressStats{MoleculeID: issue.ID, MoleculeTitle: issue.Title})
			})
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := render()
			for _, bad := range []string{"\x1b[2J", "\x1b]52;", "\x1b]0;", "\x1b[31m text"} {
				if strings.Contains(got, bad) {
					t.Errorf("terminal instructions survived: %q", got)
				}
			}
			if name != "show metadata" && !strings.Contains(ui.SanitizeForTerminal(got), "Título seguro") {
				t.Errorf("printable Unicode title lost: %q", got)
			}
		})
	}
	if !strings.Contains(issue.Title, "\x1b[2J") || !strings.Contains(issue.Assignee, "\x1b]0;") {
		t.Error("rendering mutated stored content")
	}
	b, err := json.Marshal(issue)
	if err != nil {
		t.Fatal(err)
	}
	var roundtrip types.Issue
	if err := json.Unmarshal(b, &roundtrip); err != nil {
		t.Fatal(err)
	}
	if roundtrip.Title != issue.Title || roundtrip.Assignee != issue.Assignee {
		t.Error("JSON lost original content")
	}
}
func TestDAGTerminalRenderingSanitizesBeforeTruncation(t *testing.T) {

	for _, status := range []types.Status{types.StatusOpen, types.StatusInProgress, types.StatusBlocked, types.StatusClosed} {
		t.Run(string(status), func(t *testing.T) {
			clean := "Título seguro 🛠"
			original := "\x1b]52;c;dGVzdA==\x07\x1b[2J" + clean + "\x1b]0;spoof\x1b\\\u009b\x00\x7f"
			issue := &types.Issue{ID: "bd-safe", Title: original, Status: status, Priority: 1}
			node := &GraphNode{Issue: issue}
			layout := &GraphLayout{Nodes: map[string]*GraphNode{issue.ID: node}, Layers: [][]string{{issue.ID}}, RootID: issue.ID}
			cleanNode := &GraphNode{Issue: &types.Issue{ID: issue.ID, Title: clean, Status: status, Priority: 1}}
			cleanLayout := &GraphLayout{Nodes: map[string]*GraphNode{issue.ID: cleanNode}}
			if got, want := computeDAGNodeWidth(layout), computeDAGNodeWidth(cleanLayout); got != want {
				t.Errorf("controlled title changed box width: got %d want %d", got, want)
			}
			var out strings.Builder
			renderGraphVisualTo(&out, layout, &TemplateSubgraph{Root: issue, Issues: []*types.Issue{issue}})
			got := out.String()
			for _, bad := range []string{"\x1b]52;", "\x1b[2J", "\x1b]0;", "\u009b", "\x00", "\x7f"} {
				if strings.Contains(got, bad) {
					t.Errorf("terminal instructions survived: %q", got)
				}
			}
			if !strings.Contains(ui.SanitizeForTerminal(got), clean) {
				t.Errorf("printable title lost before truncation: %q", got)
			}
			if issue.Title != original {
				t.Fatal("rendering changed stored title")
			}
			encoded, err := json.Marshal(issue)
			if err != nil {
				t.Fatal(err)
			}
			var decoded types.Issue
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.Title != original {
				t.Fatal("JSON changed stored title")
			}
		})
	}
}

func TestDAGTerminalRenderingKeepsTitlesOnOneRow(t *testing.T) {
	original := "Uno\nDos\tTres"
	issue := &types.Issue{ID: "bd-safe", Title: original, Status: types.StatusOpen}
	if err := types.ValidateIssueTitle(original); err != nil {
		t.Fatal(err)
	}
	node := &GraphNode{Issue: issue}
	line := dagNodeLine(node, 24, 1)
	if strings.ContainsAny(line, "\n\t") {
		t.Errorf("title moved terminal row or column: %q", line)
	}
	if !strings.Contains(line, "Uno Dos Tres") {
		t.Errorf("lost readable title: %q", line)
	}
	if issue.Title != original {
		t.Fatal("rendering changed stored title")
	}
}
