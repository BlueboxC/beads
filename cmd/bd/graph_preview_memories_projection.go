package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/steveyegge/beads/graphops"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/graphstore"
	"golang.org/x/text/cases"
)

const (
	graphMemoryDiscoveryOutputLimit  = 1 << 20
	graphMemoryDiscoveryQueryLimit   = 4096
	graphMemoryDiscoveryDefaultLimit = 50
	graphMemoryDiscoveryExcerptLimit = 160
)

// These are provisional summary projections, not portable Memory records or a
// public search profile. ID and Version together select the saved body to recall.
type graphMemoryDiscoveryResult struct {
	Projection string                     `json:"projection"`
	Scope      string                     `json:"scope"`
	Items      []graphMemoryDiscoveryItem `json:"items"`
	Next       *string                    `json:"next"`
	Complete   bool                       `json:"complete"`
}

type graphMemoryDiscoveryItem struct {
	ID            string                       `json:"id"`
	Type          string                       `json:"type"`
	Title         string                       `json:"title"`
	Version       string                       `json:"version"`
	Attribution   graphstore.Attribution       `json:"attribution"`
	MatchedFields []string                     `json:"matchedFields"`
	Excerpt       *graphMemoryDiscoveryExcerpt `json:"excerpt,omitempty"`
	Details       *graphMemoryDiscoveryDetails `json:"details,omitempty"`
}

type graphMemoryDiscoveryExcerpt struct {
	Field     string `json:"field"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}

type graphMemoryDiscoveryDetails struct {
	OwnedLinkCount int `json:"ownedLinkCount"`
}

// graphMemoryDiscovery consumes only an authoritative CurrentSnapshot. Storage
// has already checked all descriptors and complete records in one transaction,
// including acquisition limits before any filtering. No hydration or second
// read may be used to fill in a summary after this projection.
func graphMemoryDiscovery(scope string, snapshot graphstore.Snapshot, search string, all, details bool) (graphMemoryDiscoveryResult, error) {
	if !utf8.ValidString(search) || len(search) > graphMemoryDiscoveryQueryLimit {
		return graphMemoryDiscoveryResult{}, fmt.Errorf("%w: SEARCH must be valid UTF-8 and at most %d bytes", storage.ErrValidation, graphMemoryDiscoveryQueryLimit)
	}
	result := graphMemoryDiscoveryResult{Projection: "summary", Scope: scope, Items: []graphMemoryDiscoveryItem{}, Complete: true}
	foldedSearch := cases.Fold().String(search)
	for _, value := range snapshot.Records {
		memory, ok := value.(graphstore.Record)
		if !ok || memory.Type != graphstore.MemoryTypeURL(scope) {
			continue
		}
		fields := []string{}
		bodyMatch := -1
		if search != "" {
			if strings.Contains(cases.Fold().String(memory.Properties.Title), foldedSearch) {
				fields = append(fields, "title")
			}
			bodyMatch = strings.Index(cases.Fold().String(memory.Properties.Body), foldedSearch)
			if bodyMatch >= 0 {
				fields = append(fields, "body")
			}
			if len(fields) == 0 {
				continue
			}
		}
		if !all && len(result.Items) == graphMemoryDiscoveryDefaultLimit {
			return graphMemoryDiscoveryResult{}, fmt.Errorf("%w: more than %d Memories match; narrow SEARCH or use --all for a complete bounded result", graphstore.ErrLimitExceeded, graphMemoryDiscoveryDefaultLimit)
		}
		item := graphMemoryDiscoveryItem{ID: memory.ID, Type: memory.Type, Title: memory.Properties.Title, Version: memory.Version, Attribution: memory.Attribution, MatchedFields: fields}
		if bodyMatch >= 0 {
			item.Excerpt = graphMemoryBodyExcerpt(memory.Properties.Body, bodyMatch)
		}
		if details {
			item.Details = &graphMemoryDiscoveryDetails{OwnedLinkCount: len(memory.Owned)}
		}
		result.Items = append(result.Items, item)
	}
	sort.Slice(result.Items, func(i, j int) bool { return graphops.CompareCodeUnits(result.Items[i].ID, result.Items[j].ID) < 0 })
	// Count the actual compact preview envelope, including its newline. A caller
	// choosing another presentation must also bound its final rendered bytes.
	raw, err := json.Marshal(struct {
		SchemaVersion int                        `json:"schemaVersion"`
		Preview       bool                       `json:"preview"`
		Result        graphMemoryDiscoveryResult `json:"result"`
	}{1, true, result})
	if err != nil {
		return graphMemoryDiscoveryResult{}, err
	}
	if len(raw)+1 > graphMemoryDiscoveryOutputLimit {
		return graphMemoryDiscoveryResult{}, fmt.Errorf("%w: Memory summary response exceeds %d bytes; narrow SEARCH", graphstore.ErrLimitExceeded, graphMemoryDiscoveryOutputLimit)
	}
	return result, nil
}

// Full case folding can expand a rune (ß -> ss, İ -> i + combining dot).
// foldedByte is an index in the folded string, never an original byte offset.
// Unicode default folding is context independent; summing the folded lengths
// of original runes finds the original code point containing the first match,
// even when the match begins partway through a fold expansion.
func graphMemoryBodyExcerpt(body string, foldedByte int) *graphMemoryDiscoveryExcerpt {
	foldedOffset, matchRune := 0, 0
	folder := cases.Fold()
	for _, r := range body {
		size := len(folder.String(string(r)))
		if foldedOffset+size > foldedByte {
			break
		}
		foldedOffset += size
		matchRune++
	}
	count := utf8.RuneCountInString(body)
	start := 0
	if count > graphMemoryDiscoveryExcerptLimit && matchRune > 40 {
		start = matchRune - 40
	}
	capacity := graphMemoryDiscoveryExcerptLimit
	if start > 0 {
		capacity--
	}
	end := min(count, start+capacity)
	if end < count {
		end--
	}
	var text strings.Builder
	if start > 0 {
		text.WriteRune('…')
	}
	index := 0
	for _, r := range body {
		if index >= end {
			break
		}
		if index >= start {
			text.WriteRune(r)
		}
		index++
	}
	if end < count {
		text.WriteRune('…')
	}
	return &graphMemoryDiscoveryExcerpt{Field: "body", Text: text.String(), Truncated: start > 0 || end < count}
}
