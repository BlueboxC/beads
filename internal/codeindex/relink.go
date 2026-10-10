package codeindex

import (
	"errors"
	"io/fs"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/knowledge"
)

type RelinkDraft struct {
	Previous knowledge.Record `json:"previous"`
	Draft    knowledge.Record `json:"draft"`
}

type RelinkReport struct {
	From   string        `json:"from"`
	To     string        `json:"to"`
	SHA256 string        `json:"sha256"`
	Drafts []RelinkDraft `json:"drafts"`
	Notes  []string      `json:"notes"`
}

// PrepareRelink never writes or renews evidence. Only a unique exact-content
// move can produce drafts; each previous assertion is returned intact for review.
func (r *Reader) PrepareRelink(index Index, state knowledge.State, from, to string) (RelinkReport, error) {
	report := RelinkReport{From: from, To: to, Drafts: []RelinkDraft{}, Notes: []string{"Read current sources and every DOX before reviewing draft. Scope is recorded; previous evidence remains historical. Use knowledge record only after explicit review; existing topics cannot be overwritten by a proposal."}}
	for _, p := range []string{from, to} {
		if clean, err := safePath(p); err != nil || clean != p {
			return report, errors.New("relink requires confined canonical paths")
		}
	}
	if from == to || languageOf(from) != languageOf(to) {
		return report, errors.New("relink requires a move within the same language")
	}
	if _, err := r.root.Lstat(from); !errors.Is(err, fs.ErrNotExist) {
		return report, errors.New("old path still exists or cannot be checked; not an unambiguous move")
	}
	var target *File
	for i := range index.Files {
		if index.Files[i].Path == to {
			target = &index.Files[i]
		}
	}
	if target == nil || target.Validity != "current" || target.ParseError != "" {
		return report, errors.New("new path requires a current successfully parsed index")
	}
	for _, file := range index.Files {
		if file.Path != to && file.SHA256 == target.SHA256 {
			return report, errors.New("multiple identical destinations; review manually")
		}
	}
	report.SHA256 = target.SHA256
	names := make(map[string]bool)
	for _, symbol := range target.Symbols {
		names[symbol.ID] = true
	}
	for _, view := range state.Records {
		linked, bound := false, false
		for _, source := range view.Sources {
			if source.Path == from {
				linked = true
				bound = source.SHA256 == target.SHA256
			}
		}
		for _, id := range view.Symbols {
			linked = linked || strings.HasPrefix(id, from+"::")
		}
		if !linked {
			continue
		}
		if !bound {
			return report, errors.New("previous learning does not bind identical old content; review edited moves manually")
		}
		draft := view.Record
		draft.Sources = append([]knowledge.Source(nil), draft.Sources...)
		draft.Symbols = append([]string(nil), draft.Symbols...)
		for i := range draft.Sources {
			if draft.Sources[i].Path == from {
				draft.Sources[i].Path = to
			}
		}
		for i, id := range draft.Symbols {
			if strings.HasPrefix(id, from+"::") {
				id = to + strings.TrimPrefix(id, from)
				if !names[id] {
					return report, errors.New("destination symbol unavailable; review manually")
				}
				draft.Symbols[i] = id
			}
		}
		draft.Scope = "recorded"
		draft.Evidence = "Pending review of move " + from + " -> " + to + ". Prior " + view.Scope + " evidence (historical, not renewed):\n" + view.Evidence
		var err error
		draft, err = r.sources.Bind(draft)
		if err != nil {
			return report, err
		}
		for _, source := range draft.Sources {
			if source.Path == to && source.SHA256 != target.SHA256 {
				return report, errors.New("destination changed during preparation")
			}
		}
		checked := r.sources.Refresh(knowledge.State{Records: []knowledge.View{{Record: draft}}})
		if len(checked.Records) != 1 || checked.Records[0].Validity != "current" {
			return report, errors.New("source or DOX changed during preparation")
		}
		report.Drafts = append(report.Drafts, RelinkDraft{Previous: view.Record, Draft: draft})
	}
	sort.Slice(report.Drafts, func(i, j int) bool { return report.Drafts[i].Draft.ID < report.Drafts[j].Draft.ID })
	if len(report.Drafts) == 0 {
		return report, errors.New("no established learning references the old path")
	}
	return report, nil
}
