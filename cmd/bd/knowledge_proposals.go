package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/codeindex"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/activity"
	"github.com/steveyegge/beads/internal/knowledge"
	"github.com/steveyegge/beads/internal/ui"
)

func validateKnowledgeSymbols(record knowledge.Record) error {
	if len(record.Symbols) == 0 {
		return nil
	}
	memories, err := openMemories("symbol validation requires an initialized Beads workspace")
	if err != nil {
		return err
	}
	paths := make([]string, 0, len(record.Symbols))
	for _, symbol := range record.Symbols {
		parts := strings.SplitN(symbol, "::", 2)
		if len(parts) != 2 || parts[1] == "" {
			return errors.New("invalid code symbol reference")
		}
		paths = append(paths, parts[0])
	}
	index, err := codeindex.LoadSelected(rootCtx, memories, paths)
	if err != nil {
		return err
	}
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return ErrNoBeadsDatabase
	}
	reader, err := codeindex.Open(filepath.Dir(beadsDir))
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()
	index = reader.RefreshSelected(index)
	current := map[string]int{}
	for _, file := range index.Files {
		if file.Validity == "current" && file.ParseError == "" {
			for _, symbol := range file.Symbols {
				current[symbol.ID]++
			}
		}
	}
	for _, symbol := range record.Symbols {
		if current[symbol] != 1 {
			return fmt.Errorf("symbol %q is missing or stale; review and refresh bd code scan", symbol)
		}
	}
	return nil
}
func validateProposalRelations(record knowledge.Record, state knowledge.State) error {
	for _, related := range record.Related {
		exists := false
		for _, old := range state.Records {
			if old.ID == related {
				exists = true
			}
		}
		if !exists || related == record.ID {
			return fmt.Errorf("related record %q must exist and differ from this record", related)
		}
	}
	return nil
}
func init() {
	sources := &cobra.Command{Use: "sources <workspace-relative-path...>", Short: "Read source hashes and applicable DOX provenance without storing them", Args: cobra.RangeArgs(1, 16), RunE: func(cmd *cobra.Command, args []string) error {
		_, reader, err := openKnowledge()
		if err != nil {
			return err
		}
		defer func() { _ = reader.Close() }()
		record := knowledge.Record{ID: "source-selection", Kind: "module", Summary: "Selected provenance", Scope: "recorded"}
		for _, path := range args {
			record.Sources = append(record.Sources, knowledge.Source{Path: path})
		}
		bound, err := reader.Bind(record)
		if err != nil {
			return err
		}
		return outputJSON(bound.Sources)
	}}
	propose := &cobra.Command{Use: "propose --file <record.json>", Short: "Save a supervised record or prepared draft with inspected hashes", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("knowledge propose")
		memories, reader, err := openKnowledge()
		if err != nil {
			return err
		}
		defer func() { _ = reader.Close() }()
		file, _ := cmd.Flags().GetString("file")
		supersedes, _ := cmd.Flags().GetString("supersedes")
		var data []byte
		if file == "-" {
			data, err = io.ReadAll(io.LimitReader(os.Stdin, 24*1024+1))
		} else {
			data, err = reader.Read(file)
		}
		if err != nil {
			return err
		}
		if len(data) > 24*1024 {
			return errors.New("proposal record JSON exceeds 24 KiB")
		}
		plane, err := memories.List(rootCtx, knowledgeMemorySelection())
		if err != nil {
			return err
		}
		state := reader.Refresh(knowledge.Decode(plane.Memories))
		record, refs, err := reader.ProposalInput(data, plane.Memories, state)
		if err != nil {
			return err
		}
		if err = validateKnowledgeSymbols(record); err != nil {
			return err
		}
		if err = validateProposalRelations(record, state); err != nil {
			return err
		}
		ids, _ := cmd.Flags().GetStringArray("activity-event")
		if len(ids) > 0 {
			if len(refs) > 0 {
				return errors.New("draft already pins origins; omit --activity-event")
			}
			refs, err = activity.SelectReferences(plane.Memories, ids)
			if err != nil {
				return err
			}
		}
		proposal, err := reader.PrepareActivityProposal(record, supersedes, state, refs)
		if err != nil {
			return err
		}
		changed, err := knowledge.SaveProposal(rootCtx, memories, proposal)
		if err != nil {
			return err
		}
		if changed {
			noteDirectMemoryWrite()
		}
		if jsonOutput {
			return outputJSON(proposal)
		}
		fmt.Printf("Proposal %s saved for explicit review; no assertion accepted.\n", proposal.ID)
		return nil
	}}
	propose.Flags().String("file", "", "Workspace-relative Record/LearningDraft JSON, or - for stdin; recorded scope and inspected hashes required")
	propose.Flags().StringArray("activity-event", nil, "Observed event ID in this workspace; repeat up to 16 times. Pins reported provenance, never verified evidence")
	propose.Flags().String("supersedes", "", "Prior pending/rejected proposal ID for the same assertion; keeps its history")
	if err := propose.MarkFlagRequired("file"); err != nil {
		panic(err)
	}
	proposals := knowledgeReadCommand("proposals [module-or-issue-or-text]", "Inspect proposal status, source validity and review history", func(_ *cobra.Command, state knowledge.State) error {
		if jsonOutput {
			return outputJSON(state.Proposals)
		}
		for _, p := range state.Proposals {
			fmt.Printf("%s [%s; %s] %s: %s\n", p.ID, p.Status, p.Validity, p.Record.ID, ui.SanitizeForTerminal(p.Summary))
		}
		return nil
	})
	review := &cobra.Command{Use: "review <proposal-id>", Short: "Explicitly accept or reject an immutable supervised proposal", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("knowledge review")
		memories, reader, err := openKnowledge()
		if err != nil {
			return err
		}
		defer func() { _ = reader.Close() }()
		state, err := readKnowledge(memories, reader)
		if err != nil {
			return err
		}
		decision, _ := cmd.Flags().GetString("decision")
		reviewer, _ := cmd.Flags().GetString("reviewer")
		reason, _ := cmd.Flags().GetString("reason")
		scope, _ := cmd.Flags().GetString("scope")
		evidence, _ := cmd.Flags().GetString("evidence")
		item, err := knowledge.PrepareReview(state, args[0], decision, reviewer, reason, scope, evidence)
		if err != nil {
			return err
		}
		if item.Accepted != nil {
			if err = validateKnowledgeSymbols(*item.Accepted); err != nil {
				return err
			}
			if err = validateProposalRelations(*item.Accepted, state); err != nil {
				return err
			}
		}
		changed, err := knowledge.SaveReview(rootCtx, memories, item)
		if err != nil {
			return err
		}
		if changed {
			noteDirectMemoryWrite()
		}
		if jsonOutput {
			return outputJSON(item)
		}
		fmt.Printf("Review %s saved (%s). Refresh proposals to inspect concurrent conflicts; no verification was executed.\n", item.ID, item.Decision)
		return nil
	}}
	for _, name := range []string{"decision", "reviewer", "reason", "scope", "evidence"} {
		review.Flags().String(name, "", map[string]string{"decision": "accept or reject", "reviewer": "Human or supervising agent identity (a label, not authentication)", "reason": "Explicit review rationale", "scope": "Explicit acceptance scope: recorded, inspected, tested, installed or productive", "evidence": "Evidence supporting the scope; never inferred or executed"}[name])
	}
	for _, name := range []string{"decision", "reviewer", "reason"} {
		if err := review.MarkFlagRequired(name); err != nil {
			panic(err)
		}
	}
	knowledgeCmd.AddCommand(sources, propose, proposals, review)
}
