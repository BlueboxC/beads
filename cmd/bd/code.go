package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/codeindex"
	"github.com/steveyegge/beads/internal/graphview"
	"github.com/steveyegge/beads/internal/knowledge"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/memoryops"
)

func openCodeIndex() (memoryops.Memories, *codeindex.Reader, map[string]string, codeindex.Index, error) {
	memories, err := openMemories("code requires an initialized Beads workspace")
	if err != nil {
		return nil, nil, nil, codeindex.Index{}, err
	}
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return nil, nil, nil, codeindex.Index{}, ErrNoBeadsDatabase
	}
	plane, err := memories.List(rootCtx, memoryops.ListRequest{})
	if err != nil {
		return nil, nil, nil, codeindex.Index{}, err
	}
	index, err := codeindex.Load(plane.Memories)
	if err != nil {
		return nil, nil, nil, index, err
	}
	reader, err := codeindex.Open(filepath.Dir(beadsDir))
	return memories, reader, plane.Memories, index, err
}

var codeCmd = &cobra.Command{
	Use: "code", GroupID: "advanced", Short: "Code structure and references in the existing Dolt (fork extension)",
	Long: `Maintain a regenerable Python/Go/JavaScript/TypeScript index beside human knowledge in the
same Beads Dolt memory plane. Scan parses supplied text through Python's stdlib, Go's parser or a pinned
TypeScript parser in an isolated Node process; it never imports or executes project code. Query by path,
symbol or linked knowledge. Static references do not prove runtime reachability.
No model, daemon, embeddings, schema migration or second database.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		licenses, err := cmd.Flags().GetBool("licenses")
		if err != nil {
			return err
		}
		if licenses {
			_, err = fmt.Fprint(cmd.OutOrStdout(), codeindex.ParserLicenses())
			return err
		}
		return cmd.Help()
	},
}

func init() {
	codeCmd.Flags().Bool("licenses", false, "Show bundled parser licenses and notices")
	scan := &cobra.Command{
		Use: "scan [workspace-relative-code-file-or-directory...]", Short: "Incrementally parse selected code; preserve human records",
		RunE: func(cmd *cobra.Command, args []string) error {
			CheckReadonly("code scan")
			started := time.Now()
			memories, reader, plane, previous, err := openCodeIndex()
			if err != nil {
				return err
			}
			defer func() { _ = reader.Close() }()
			exclude, err := cmd.Flags().GetStringSlice("exclude")
			if err != nil {
				return err
			}
			if len(args) == 0 {
				args = previous.Roots
				if !cmd.Flags().Changed("exclude") {
					exclude = previous.Exclude
				}
			}
			rebuild, err := cmd.Flags().GetBool("rebuild")
			if err != nil {
				return err
			}
			python, err := cmd.Flags().GetString("python")
			if err != nil {
				return err
			}
			languages, err := cmd.Flags().GetStringSlice("languages")
			if err != nil {
				return err
			}
			if !cmd.Flags().Changed("languages") {
				languages = previous.Languages
			}
			node, err := cmd.Flags().GetString("node")
			if err != nil {
				return err
			}
			index, err := reader.ScanWithOptions(rootCtx, args, exclude, previous, codeindex.ScanOptions{Languages: languages, Python: python, Node: node, Rebuild: rebuild})
			if err != nil {
				return err
			}
			stored, err := codeindex.Save(rootCtx, memories, plane, index)
			if err != nil {
				return err
			}
			noteDirectMemoryWrite()
			index.Stats.StoredBytes = stored
			result := struct {
				Stats     codeindex.Stats   `json:"stats"`
				Changes   codeindex.Changes `json:"changes"`
				ElapsedMS int64             `json:"elapsed_ms"`
				Roots     []string          `json:"roots"`
				Exclude   []string          `json:"exclude"`
				Languages []string          `json:"languages"`
			}{index.Stats, index.Changes, time.Since(started).Milliseconds(), index.Roots, index.Exclude, index.Languages}
			if jsonOutput {
				return outputJSON(result)
			}
			fmt.Printf("Indexed %d code files, %d symbols; parsed %d, reused %d, parse errors %d. %d stored bytes; %d ms. Human records unchanged.\n", index.Stats.Files, index.Stats.Symbols, index.Changes.Parsed, index.Changes.Reused, index.Stats.ParseErrors, stored, result.ElapsedMS)
			return nil
		},
	}
	scan.Flags().StringSlice("exclude", nil, "Workspace-relative subtrees excluded from selection")
	scan.Flags().Bool("rebuild", false, "Reparse all selected code; never rebind human assertions")
	scan.Flags().StringSlice("languages", nil, "Explicit languages: python, go, javascript, typescript; reuse previous selection, default Python")
	scan.Flags().String("node", "node", "Operator-owned Node.js for the bundled JS/TS parser")
	scan.Flags().String("python", "python3", "Operator-owned Python 3 interpreter for isolated AST parsing")
	status := &cobra.Command{Use: "status", Short: "Check persisted index against current code and DOX", Args: cobra.NoArgs, RunE: func(_ *cobra.Command, _ []string) error {
		_, reader, _, index, err := openCodeIndex()
		if err != nil {
			return err
		}
		defer func() { _ = reader.Close() }()
		if index.Version == 0 {
			return errors.New("no code index; select roots with bd code scan")
		}
		index = reader.Refresh(index)
		refs := codeindex.Relations(index)
		resolved := 0
		for _, ref := range refs {
			if ref.Resolution == "static_reference" {
				resolved++
			}
		}
		result := struct {
			Stats            codeindex.Stats `json:"stats"`
			Roots            []string        `json:"roots"`
			Exclude          []string        `json:"exclude"`
			Python           string          `json:"python_version"`
			Languages        []string        `json:"languages"`
			Node             string          `json:"node_version,omitempty"`
			TypeScript       string          `json:"typescript_version,omitempty"`
			StaticReferences int             `json:"static_references"`
			Unresolved       int             `json:"unresolved_references"`
			Warnings         []string        `json:"warnings"`
		}{index.Stats, index.Roots, index.Exclude, index.Python, index.Languages, index.Node, index.TypeScript, resolved, len(refs) - resolved, index.Warnings}
		if jsonOutput {
			return outputJSON(result)
		}
		fmt.Printf("%d files; %d symbols; %d static references; %d unresolved; %d parse errors.\n", index.Stats.Files, index.Stats.Symbols, resolved, len(refs)-resolved, index.Stats.ParseErrors)
		for _, warning := range index.Warnings {
			fmt.Println(ui.SanitizeForTerminal(warning))
		}
		return nil
	}}
	read := func(use, short string, graph bool) *cobra.Command {
		command := &cobra.Command{Use: use, Short: short, Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			_, reader, plane, index, err := openCodeIndex()
			if err != nil {
				return err
			}
			defer func() { _ = reader.Close() }()
			if index.Version == 0 {
				return errors.New("no code index; select roots with bd code scan")
			}
			index = reader.Refresh(index)
			beadsDir := beads.FindBeadsDir()
			sourceReader, err := knowledge.Open(filepath.Dir(beadsDir))
			if err != nil {
				return err
			}
			defer func() { _ = sourceReader.Close() }()
			state := sourceReader.Refresh(knowledge.Decode(plane))
			needle := ""
			if len(args) == 1 {
				needle = args[0]
			}
			limit, err := cmd.Flags().GetInt("limit")
			if err != nil {
				return err
			}
			if limit < 1 || limit > 1024 {
				return errors.New("limit must be 1-1024 files")
			}
			query := codeindex.Select(index, state, needle, limit)
			if graph {
				page, err := codeindex.Graph(query)
				if err != nil {
					return err
				}
				html, err := cmd.Flags().GetBool("html")
				if err != nil {
					return err
				}
				if html {
					if jsonOutput {
						return errors.New("choose --html or --json")
					}
					return graphview.WriteHTML(os.Stdout, page)
				}
				return outputJSON(page)
			}
			if jsonOutput {
				return outputJSON(query)
			}
			for _, file := range query.Files {
				fmt.Printf("%s [%s] sha256=%s\n", ui.SanitizeForTerminal(file.Path), file.Validity, file.SHA256)
				for _, symbol := range file.Symbols {
					fmt.Printf("  %s:%d %s\n", ui.SanitizeForTerminal(file.Path), symbol.Line, ui.SanitizeForTerminal(symbol.Name))
				}
			}
			for _, view := range query.Knowledge {
				fmt.Printf("Knowledge %s [%s; %s]: %s\n", ui.SanitizeForTerminal(view.ID), view.Scope, view.Validity, ui.SanitizeForTerminal(view.Summary))
			}
			for _, warning := range query.Warnings {
				fmt.Println(ui.SanitizeForTerminal(warning))
			}
			return nil
		}}
		command.Flags().Int("limit", 50, "Maximum matching files; omitted matches are reported")
		if graph {
			command.Flags().Bool("html", false, "Output the native offline interactive viewer")
		}
		return command
	}
	codeCmd.AddCommand(scan, status, read("query [path-or-symbol-or-learning]", "Inspect symbols, static/unresolved references and human learnings", false), read("graph [path-or-symbol-or-learning]", "Export the native code graph snapshot (not task blockers)", true))
	rootCmd.AddCommand(codeCmd)
}
