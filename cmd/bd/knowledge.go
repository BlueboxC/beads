package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/codeindex"
	"github.com/steveyegge/beads/internal/knowledge"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/memoryops"
)

// Source paths follow the selected Beads workspace, not the caller's cwd.
func openKnowledge() (memoryops.Memories, *knowledge.Reader, error) {
	memories, err := openMemories("knowledge requires an initialized Beads workspace")
	if err != nil {
		return nil, nil, err
	}
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return nil, nil, ErrNoBeadsDatabase
	}
	reader, err := knowledge.Open(filepath.Dir(beadsDir))
	return memories, reader, err
}

func readKnowledge(memories memoryops.Memories, reader *knowledge.Reader) (knowledge.State, error) {
	result, err := memories.List(rootCtx, knowledgeMemorySelection())
	if err != nil {
		return knowledge.State{}, err
	}
	return reader.Refresh(knowledge.Decode(result.Memories)), nil
}

var knowledgeCmd = &cobra.Command{
	Use:     "knowledge",
	GroupID: "advanced",
	Short:   "Source-backed project context (fork extension)",
	Long: `Index selected documents and explicitly record objectives, decisions,
modules and solutions in Beads' existing Dolt memory plane. Source hashes and
DOX ancestry detect stale evidence. No model calls or document execution.

Documents are source material; scan does not infer verified learnings.
Use record for reviewed assertions, and context for bounded session recovery.`,
}

var knowledgeScanCmd = &cobra.Command{
	Use:   "scan [workspace-relative-document-or-directory...]",
	Short: "Register or refresh selected documents and DOX ancestors",
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("knowledge scan")
		memories, reader, err := openKnowledge()
		if err != nil {
			return err
		}
		defer func() { _ = reader.Close() }()
		if len(args) == 0 {
			plane, err := memories.List(rootCtx, knowledgeMemorySelection())
			if err != nil {
				return err
			}
			catalog, err := knowledge.LoadCatalog(plane.Memories)
			if err != nil {
				return err
			}
			args = catalog.Roots
			if len(args) == 0 {
				args = []string{"DOX.md"}
			}
		}
		catalog, err := reader.Scan(args)
		if err != nil {
			return err
		}
		if err := knowledge.SaveCatalog(rootCtx, memories, catalog); err != nil {
			return err
		}
		noteDirectMemoryWrite()
		if jsonOutput {
			return outputJSON(catalog)
		}
		fmt.Printf("Indexed %d documents in the existing Beads database. Assertions are unchanged.\n", len(catalog.Sources))
		return nil
	},
}

var knowledgeRecordCmd = &cobra.Command{
	Use:   "record --file <record.json>",
	Short: "Store a reviewed assertion, binding current sources and DOX contracts",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("knowledge record")
		memories, reader, err := openKnowledge()
		if err != nil {
			return err
		}
		defer func() { _ = reader.Close() }()
		file, err := cmd.Flags().GetString("file")
		if err != nil {
			return err
		}
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
			return errors.New("record JSON exceeds 24 KiB")
		}
		var record knowledge.Record
		if err := json.Unmarshal(data, &record); err != nil {
			return err
		}
		if err := validateKnowledgeSymbols(record); err != nil {
			return err
		}
		record, err = reader.Bind(record)
		if err != nil {
			return err
		}
		state, err := readKnowledge(memories, reader)
		if err != nil {
			return err
		}
		found := false
		for _, existing := range state.Records {
			if existing.ID == record.ID {
				found = true
			}
		}
		if !found && len(state.Records) >= knowledge.MaxRecords {
			return fmt.Errorf("at most %d knowledge records; consolidate or explicitly forget an old record", knowledge.MaxRecords)
		}
		if err := validateProposalRelations(record, state); err != nil {
			return err
		}
		if err := knowledge.SaveRecord(rootCtx, memories, record); err != nil {
			return err
		}
		noteDirectMemoryWrite()
		if jsonOutput {
			return outputJSON(record)
		}
		fmt.Printf("Recorded %s [%s; %s]. Source hashes bound; no verification was executed.\n", ui.SanitizeForTerminal(record.ID), record.Kind, record.Scope)
		return nil
	},
}

func knowledgeReadCommand(use, short string, render func(*cobra.Command, knowledge.State) error) *cobra.Command {
	return &cobra.Command{
		Use: use, Short: short, Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			memories, reader, err := openKnowledge()
			if err != nil {
				return err
			}
			defer func() { _ = reader.Close() }()
			state, err := readKnowledge(memories, reader)
			if err != nil {
				return err
			}
			if cmd.Name() == "context" {
				state = reader.Discover(state)
			}
			if len(args) == 1 {
				state = knowledge.Filter(state, args[0])
			}
			return render(cmd, state)
		},
	}
}

func init() {
	list := knowledgeReadCommand("list [module-or-issue-or-text]", "Inspect records and current source validity", func(_ *cobra.Command, state knowledge.State) error {
		if jsonOutput {
			return outputJSON(state)
		}
		for _, record := range state.Records {
			fmt.Printf("%s [%s; %s; %s] %s\n", record.ID, record.Kind, record.Scope, record.Validity, ui.SanitizeForTerminal(record.Summary))
		}
		for _, warning := range state.Warnings {
			fmt.Println(ui.SanitizeForTerminal(warning))
		}
		fmt.Printf("%d records; %d indexed documents.\n", len(state.Records), len(state.Catalog.Sources))
		return nil
	})
	contextCmd := knowledgeReadCommand("context [module-or-issue-or-text]", "Recover bounded project direction and relevant learnings", func(_ *cobra.Command, state knowledge.State) error {
		if jsonOutput {
			return outputJSON(state)
		}
		fmt.Print(ui.SanitizeForTerminal(knowledge.Context(state)))
		return nil
	})
	graph := knowledgeReadCommand("graph [module-or-issue-or-text]", "Export stored knowledge relationships as JSON or offline HTML", func(cmd *cobra.Command, state knowledge.State) error {
		htmlOutput, err := cmd.Flags().GetBool("html")
		if err != nil {
			return err
		}
		data := knowledge.BuildGraph(state)
		if !htmlOutput {
			return outputJSON(data)
		}
		if jsonOutput {
			return errors.New("choose --html or --json")
		}
		return knowledge.WriteHTML(os.Stdout, data)
	})
	graph.Flags().Bool("html", false, "Output offline interactive HTML (redirect to a private file)")
	knowledgeRecordCmd.Flags().String("file", "", "Workspace-relative JSON assertion file, or - for stdin; sources are workspace-relative")
	if err := knowledgeRecordCmd.MarkFlagRequired("file"); err != nil {
		panic(err)
	}
	knowledgeCmd.AddCommand(knowledgeScanCmd, knowledgeRecordCmd, list, contextCmd, graph)
	rootCmd.AddCommand(knowledgeCmd)
}

// prime already read this memory plane through the direct/proxied role.
// Reuse that read and omit structured values from the ordinary memory renderer.
func knowledgeForPrime(plane map[string]string) (map[string]string, string) {
	plain := make(map[string]string)
	hasKnowledge := false
	for key, value := range plane {
		if knowledge.IsKey(key) {
			hasKnowledge = true
		} else {
			plain[key] = value
		}
	}
	if !hasKnowledge {
		return plain, ""
	}
	state := knowledge.Decode(plane)
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return plain, "\nProject knowledge unavailable: workspace root unresolved.\n"
	}
	reader, err := knowledge.Open(filepath.Dir(beadsDir))
	if err != nil {
		return plain, "\nProject knowledge unavailable: source root could not be opened.\n"
	}
	defer func() { _ = reader.Close() }()
	state = reader.Discover(reader.Refresh(state))
	projection := codeindex.Summary(plane) + knowledge.Context(state)
	if len(projection) > 6400 {
		projection = projection[:6300]
		for !utf8.ValidString(projection) {
			projection = projection[:len(projection)-1]
		}
		projection += "\nAdditional context omitted; query bd knowledge context or bd code query.\n"
	}
	return plain, ui.SanitizeForTerminal(projection)
}

// Knowledge and observed activity share the metadata namespace. Code blobs,
// including retained historical generations, are never learning input.
func knowledgeMemorySelection() memoryops.ListRequest {
	return memoryops.ListRequest{KeyPrefix: "@", ExcludeKeyPrefix: codeindex.Prefix}
}
