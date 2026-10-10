package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/graphstore"
	"github.com/steveyegge/beads/internal/utils"
	"github.com/steveyegge/beads/internal/validation"
)

const graphIssueListOutputLimit = 16 << 20

var errGraphListSelector = errors.New("invalid Issue list selector")

func runGraphPreviewList(cmd *cobra.Command) error {
	// gatherListInput lowers the Changed state of the Issue filters once it has read
	// them, so what the caller typed is read here, before anything gathers.
	typed := graphIssueListTypedOptions(cmd)
	in, structured, err := graphIssueListInput(cmd, os.Args[1:])
	if err != nil {
		return err
	}
	selectedType := ""
	if cmd.Flags().Changed("bead-type") {
		selector, _ := cmd.Flags().GetString("bead-type")
		selectedType, err = graphPreviewTypeURL(graphPreviewConfig.GraphScopeURL, selector)
		if err != nil {
			return graphFailure("invalid_selector", err.Error(), 2)
		}
	}
	plan := planGraphList(typed, graphIssueListConfigLabel(cmd, in), selectedType, graphstore.IssueTypeURL(graphPreviewConfig.GraphScopeURL))
	if plan.refuse {
		return graphFailure("capability_unavailable", "Issue list filters cannot be combined with a non-Issue --bead-type", 5)
	}
	if !plan.issueQuery {
		return withGraphStoreOutput(func(ctx context.Context, s *graphstore.Store) (any, string, error) {
			page, err := s.ListBeads(ctx, graphstore.BeadListRequest{TypeURL: selectedType, All: in.AllFlag, Limit: *in.Limit, MaxRows: in.MaxRows})
			if err != nil {
				return nil, "", err
			}
			output, err := renderGraphBeadList(page, s.ScopeURL(), in.AllFlag, plan.notice, structured, quietFlag)
			return nil, output, err
		}, func(_ any, output string) error { _, err := fmt.Fprint(cmd.OutOrStdout(), output); return err })
	}
	return withGraphStoreOutput(func(ctx context.Context, s *graphstore.Store) (any, string, error) {
		page, err := s.ListIssues(ctx, in.ListRequest)
		if errors.Is(err, storage.ErrValidation) {
			return nil, "", fmt.Errorf("%w: %w", errGraphListSelector, err)
		}
		if err != nil {
			return nil, "", err
		}
		output, err := renderGraphIssueList(page, plan.notice, structured, quietFlag)
		return nil, output, err
	}, func(_ any, output string) error { _, err := fmt.Fprint(cmd.OutOrStdout(), output); return err })
}

func graphIssueListInput(cmd *cobra.Command, argv []string) (listInput, bool, error) {
	fail := func(code, message string, exit int) (listInput, bool, error) {
		return listInput{}, false, graphFailure(code, message, exit)
	}
	if err := graphPreviewFlags(cmd, "flat", "format", "bead-type", "status", "state", "type", "all", "limit", "title", "title-contains", "priority", "priority-min", "priority-max", "label", "label-any", "exclude-label", "pinned", "no-pinned", "sort", "reverse", "assignee", "no-assignee", "due-before", "due-after", "overdue"); err != nil {
		return listInput{}, false, err
	}
	format, _ := cmd.Flags().GetString("format")
	structured := format == "records-json"
	if cmd.Flags().Changed("json") || (jsonOutput && !structured) {
		return fail("capability_unavailable", "graph list preserves the Issue JSON compatibility gate; use --format records-json for experimental graph records", 5)
	}
	flat, _ := cmd.Flags().GetBool("flat")
	if (cmd.Flags().Changed("format") && !structured) || (cmd.Flags().Changed("flat") && !flat) {
		return fail("capability_unavailable", "graph list supports flat output or --format records-json; tree and legacy JSON are unavailable", 5)
	}
	if err := graphIssueListRepeatedFilters(cmd, argv); err != nil {
		return fail("capability_unavailable", err.Error(), 5)
	}
	if cmd.Flags().Changed("status") && cmd.Flags().Changed("state") {
		return fail("invalid_selector", "graph list accepts --status or --state, not both", 2)
	}
	for _, name := range []string{"status", "state", "type", "title", "title-contains", "sort", "assignee", "due-before", "due-after"} {
		value, _ := cmd.Flags().GetString(name)
		if !utf8.ValidString(value) || len(value) > 4096 {
			return fail("invalid_selector", "--"+name+" must be UTF-8 and at most 4096 bytes", 2)
		}
	}
	sort, _ := cmd.Flags().GetString("sort")
	if !map[string]bool{"": true, "priority": true, "created": true, "updated": true, "title": true, "status": true, "type": true}[sort] {
		return fail("capability_unavailable", "graph list supports sort priority, created, updated, title, status or type", 5)
	}
	for _, name := range []string{"priority", "priority-min", "priority-max"} {
		if cmd.Flags().Changed(name) {
			value, _ := cmd.Flags().GetString(name)
			if _, err := validation.ValidatePriority(value); err != nil {
				return fail("invalid_selector", err.Error(), 2)
			}
		}
	}
	pinned, _ := cmd.Flags().GetBool("pinned")
	unpinned, _ := cmd.Flags().GetBool("no-pinned")
	if pinned && unpinned {
		return fail("invalid_selector", "--pinned and --no-pinned are mutually exclusive", 2)
	}
	for _, name := range []string{"label", "label-any", "exclude-label"} {
		if !cmd.Flags().Changed(name) {
			continue
		}
		values, _ := cmd.Flags().GetStringSlice(name)
		if len(values) > 256 {
			return fail("invalid_selector", "too many label values", 2)
		}
		for _, value := range values {
			if !utf8.ValidString(value) || len(value) > 4096 {
				return fail("invalid_selector", "labels must be UTF-8 and at most 4096 bytes each", 2)
			}
		}
		if len(utils.NormalizeLabels(values)) == 0 {
			return fail("invalid_selector", "--"+name+" requires a nonempty label", 2)
		}
	}
	// Reuse the existing limit/config/directory-label policy after unsupported
	// inputs are fenced. Neither the ordinary parser nor its output is replaced.
	in, err := gatherListInput(cmd)
	if err != nil {
		return listInput{}, false, err
	}
	// pflag returns a non-nil empty slice for an omitted StringSlice flag.
	// Admission already refused explicit --exclude-type, including an empty one.
	// Canonicalize only the absent value before the strict storage API boundary.
	if !cmd.Flags().Changed("exclude-type") && len(in.ExcludeTypes) == 0 {
		in.ExcludeTypes = nil
	}
	if in.Limit == nil || *in.Limit < 0 {
		return fail("invalid_selector", "list limit must be non-negative", 2)
	}
	return in, structured, nil
}

// A read-only replay detects spellings the current ordinary parser collapses.
// It does not replace contributor-owned flag values or implement union semantics.
// pflag itself consumes values, optional values and short aliases, so text inside
// a title is never mistaken for another flag. The original FlagSet is untouched.
func graphIssueListRepeatedFilters(cmd *cobra.Command, argv []string) error {
	probe := pflag.NewFlagSet("graph-list-admission", pflag.ContinueOnError)
	probe.SetOutput(io.Discard)
	counts := map[string]*graphIssueListFlagCount{}
	add := func(f *pflag.Flag) {
		if probe.Lookup(f.Name) != nil {
			return
		}
		value := &graphIssueListFlagCount{kind: f.Value.Type()}
		counts[f.Name] = value
		probe.AddFlag(&pflag.Flag{Name: f.Name, Shorthand: f.Shorthand, Value: value, NoOptDefVal: f.NoOptDefVal})
	}
	cmd.Flags().VisitAll(add)
	cmd.InheritedFlags().VisitAll(add)
	if err := probe.Parse(argv); err != nil {
		return fmt.Errorf("cannot validate graph list filter occurrences: %w", err)
	}
	for _, name := range []string{"status", "state", "type", "assignee", "bead-type"} {
		if count := counts[name]; count != nil && count.n > 1 {
			return fmt.Errorf("graph list does not yet support repeated --%s; supply one filter", name)
		}
	}
	return nil
}

type graphIssueListFlagCount struct {
	kind string
	n    int
}

func (v *graphIssueListFlagCount) Set(string) error { v.n++; return nil }
func (v *graphIssueListFlagCount) String() string   { return "" }
func (v *graphIssueListFlagCount) Type() string     { return v.kind }

// The existing Issue filters keep their native query and result contract.
// An unfiltered graph list, or one narrowed only by nominal Bead Type, reads
// the complete checked current inventory and projects its Beads. The options
// below select the Issue query instead, and the notice names them in this order.
var graphIssueListOptions = []string{"status", "state", "type", "title", "title-contains", "priority", "priority-min", "priority-max", "label", "label-any", "exclude-label", "pinned", "no-pinned", "sort", "reverse", "assignee", "no-assignee", "due-before", "due-after", "overdue"}

// graphIssueListTypedOptions names the Issue options the caller supplied, in
// canonical order. A supplied flag counts even when its value, such as an empty
// --assignee=, adds no restriction.
func graphIssueListTypedOptions(cmd *cobra.Command) []string {
	typed := []string{}
	for _, name := range graphIssueListOptions {
		if cmd.Flags().Changed(name) {
			typed = append(typed, name)
		}
	}
	return typed
}

// graphIssueListConfigLabel is the directory label the ordinary list parser
// applied from configuration, which it does only while neither --label nor
// --label-any was typed. Implicit directory labels must not be silently ignored,
// so it selects the Issue query like a typed option does.
func graphIssueListConfigLabel(cmd *cobra.Command, in listInput) string {
	if len(in.LabelsAny) == 0 || cmd.Flags().Changed("label") || cmd.Flags().Changed("label-any") {
		return ""
	}
	return in.LabelsAny[0]
}

// graphListPlan says which query a list runs and what its human output tells
// the caller about that choice.
type graphListPlan struct {
	issueQuery bool   // read through the native Issue query rather than the Bead inventory
	refuse     bool   // typed Issue options cannot be combined with a non-Issue Bead Type
	notice     string // line directly under the human header, empty for none
}

// planGraphList routes a list. Typed Issue options select the Issue query and a
// non-Issue Bead Type refuses them. A configured directory label also selects
// it, but a Memory carries no labels, so the label never refuses a Bead Type: it
// is simply not applied to one, and the notice says so. Naming the Issue Type
// already narrowed the list to Issues, so that route needs no notice.
func planGraphList(typed []string, label, selectedType, issueType string) graphListPlan {
	switch {
	case len(typed) > 0 && selectedType != "" && selectedType != issueType:
		return graphListPlan{refuse: true}
	case len(typed) > 0 || (label != "" && (selectedType == "" || selectedType == issueType)):
		plan := graphListPlan{issueQuery: true}
		if selectedType == "" {
			plan.notice = graphIssueQueryNotice(typed, label)
		}
		return plan
	case label != "":
		return graphListPlan{notice: fmt.Sprintf("directory.labels %q is not applied to this Bead Type.", label)}
	}
	return graphListPlan{}
}

func graphIssueQueryNotice(typed []string, label string) string {
	selectedBy := make([]string, 0, len(typed)+1)
	for _, name := range typed {
		selectedBy = append(selectedBy, "--"+name)
	}
	if label != "" {
		selectedBy = append(selectedBy, fmt.Sprintf("directory.labels %q", label))
	}
	return "Memories are not listed (Issue query selected by: " + strings.Join(selectedBy, ", ") + ")."
}

func renderGraphBeadList(page graphstore.BeadListPage, scope string, all bool, notice string, structured, quiet bool) (string, error) {
	var human strings.Builder
	if !structured && !quiet {
		fmt.Fprintf(&human, "Beads (%d; more: %t; graph preview)\n", len(page.Items), page.HasMore)
		if notice != "" {
			human.WriteString(notice + "\n")
		}
		for _, value := range page.Items {
			switch record := value.(type) {
			case graphstore.Record:
				fmt.Fprintf(&human, "  %s  Memory  %s\n", graphMemoryDisplayText(strings.TrimPrefix(record.ID, scope)), graphMemoryDisplayText(record.Properties.Title))
			case graphstore.IssueRecord:
				fmt.Fprintf(&human, "  %s  Issue   %s  P%d  %s\n", graphMemoryDisplayText(strings.TrimPrefix(record.ID, scope)), graphMemoryDisplayText(string(record.Properties.Status)), record.Properties.Priority, graphMemoryDisplayText(record.Properties.Title))
			}
		}
		if page.HasMore {
			if all {
				human.WriteString("More Beads exist; increase --limit within preview bounds.\n")
			} else {
				human.WriteString("More Beads exist; increase --limit or use --all within preview bounds.\n")
			}
		}
	}
	var output bytes.Buffer
	if err := graphPrintTo(&output, page, strings.TrimSuffix(human.String(), "\n"), quiet, structured); err != nil {
		return "", err
	}
	if output.Len() > graphIssueListOutputLimit {
		return "", fmt.Errorf("%w: Bead list output exceeds %d bytes; narrow the query", graphstore.ErrLimitExceeded, graphIssueListOutputLimit)
	}
	return output.String(), nil
}

func renderGraphIssueList(page graphstore.IssueListPage, notice string, structured, quiet bool) (string, error) {
	var human strings.Builder
	if !structured && !quiet {
		fmt.Fprintf(&human, "Issues (%d; more: %t; graph preview)\n", len(page.Items), page.HasMore)
		if notice != "" {
			human.WriteString(notice + "\n")
		}
		for _, item := range page.Items {
			fmt.Fprintf(&human, "%q %q P%d %q\n", item.ID, item.Properties.Status, item.Properties.Priority, item.Properties.Title)
		}
		if page.HasMore {
			human.WriteString("More matching Issues exist; increase --limit or use --all within preview bounds.\n")
		}
	}
	var output bytes.Buffer
	if err := graphPrintTo(&output, page, strings.TrimSuffix(human.String(), "\n"), quiet, structured); err != nil {
		return "", err
	}
	if output.Len() > graphIssueListOutputLimit {
		return "", fmt.Errorf("%w: Issue list output exceeds %d bytes; narrow the query", graphstore.ErrLimitExceeded, graphIssueListOutputLimit)
	}
	return output.String(), nil
}
