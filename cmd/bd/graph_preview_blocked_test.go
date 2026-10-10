package main

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/steveyegge/beads/internal/storage/graphstore"
	"github.com/steveyegge/beads/internal/types"
)

func blockedPreviewCommand(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	// Copy the registered definitions with independent parser values. In
	// particular StringSlice has private append state beyond Flag.Changed, so
	// mutating/restoring shared values would contaminate later command tests.
	cmd := &cobra.Command{RunE: blockedCmd.RunE}
	values := pflag.NewFlagSet("blocked-test-values", pflag.ContinueOnError)
	clone := func(flag *pflag.Flag) {
		switch flag.Value.Type() {
		case "string":
			values.String(flag.Name, flag.DefValue, flag.Usage)
		case "stringSlice":
			values.StringSlice(flag.Name, nil, flag.Usage)
		case "bool":
			values.Bool(flag.Name, false, flag.Usage)
		default:
			t.Fatalf("clone registered blocked flag --%s type %s explicitly", flag.Name, flag.Value.Type())
		}
		copied := *flag
		copied.Value = values.Lookup(flag.Name).Value
		copied.Changed = false
		cmd.Flags().AddFlag(&copied)
	}
	blockedCmd.LocalNonPersistentFlags().VisitAll(clone)
	// Use the real global definitions for the output/read-only controls exercised
	// here too, without sharing their pointers to process-global configuration.
	for _, name := range []string{"json", "readonly", "quiet"} {
		flag := rootCmd.PersistentFlags().Lookup(name)
		if flag == nil {
			t.Fatalf("registered global flag --%s missing", name)
		}
		if cmd.Flags().Lookup(name) == nil {
			clone(flag)
		}
	}
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatal(err)
	}
	return cmd
}

func TestGraphPreviewBlockedAdmission(t *testing.T) {
	savedJSON, savedStructured := jsonOutput, graphPreviewStructuredErrors
	jsonOutput, graphPreviewStructuredErrors = false, false
	t.Cleanup(func() { jsonOutput, graphPreviewStructuredErrors = savedJSON, savedStructured })
	for _, tc := range []struct {
		name        string
		flags, args []string
		env         string
		code        int
	}{
		{name: "default"}, {name: "readonly", flags: []string{"--readonly"}}, {name: "quiet-json", flags: []string{"--quiet", "--json"}}, {name: "zero-cap", env: "0"},
		{name: "selector", args: []string{"beads/work"}, code: 2},
		{name: "parent", flags: []string{"--parent=beads/p"}, code: 5},
		{name: "empty-parent", flags: []string{"--parent="}, code: 5},
		{name: "label", flags: []string{"--label=demo"}},
		{name: "short-label", flags: []string{"-l", "demo"}},
		{name: "csv-label", flags: []string{"--label=one,two"}},
		{name: "label-any", flags: []string{"--label-any=demo"}},
		{name: "exclude-label", flags: []string{"--exclude-label=demo"}},
		{name: "blank-label", flags: []string{"--label="}, code: 2},
		{name: "blank-label-any", flags: []string{"--label-any="}, code: 2},
		{name: "blank-exclude-label", flags: []string{"--exclude-label="}, code: 2},
		{name: "positive-cap", env: "1", code: 5}, {name: "bad-cap", env: "bad", code: 2}, {name: "negative-cap", env: "-1", code: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(maxRowsEnvVar, tc.env)
			err := graphPreviewBlockedInput(blockedPreviewCommand(t, tc.flags...), tc.args)
			if tc.code == 0 {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var exit *exitError
			if !errors.As(err, &exit) || exit.Code != tc.code {
				t.Fatalf("expected exit%d before store access: %v", tc.code, err)
			}
		})
	}
}

func TestGraphPreviewBlockedOutput(t *testing.T) {
	item := graphstore.BlockedIssue{Issue: graphstore.IssueRecord{ID: "https://example.test/beads/work", Revision: "v1", Version: "v1", Properties: &types.Issue{ID: "native-private-property", Title: "Line\n雪", Priority: 1}, Metadata: json.RawMessage(`{}`), Owned: []json.RawMessage{}}, BlockedBy: []string{"https://example.test/beads/prereq"}}
	for _, quiet := range []bool{false, true} {
		output, err := renderGraphIssueBlocked([]graphstore.BlockedIssue{item}, true, quiet)
		if err != nil {
			t.Fatal(err)
		}
		var envelope struct {
			SchemaVersion int
			Preview       bool
			Result        []graphstore.BlockedIssue
		}
		want := item
		want.Issue.Version = "" // Complete CLI records expose revision only.
		if err := json.Unmarshal([]byte(output), &envelope); err != nil || envelope.SchemaVersion != 1 || !envelope.Preview || !reflect.DeepEqual(envelope.Result, []graphstore.BlockedIssue{want}) || strings.Contains(output, `"version":"v1"`) || !strings.Contains(output, `"metadata":{}`) {
			t.Fatalf("complete graph envelope changed: %s %v", output, err)
		}
	}
	output, err := renderGraphIssueBlocked([]graphstore.BlockedIssue{item}, false, false)
	want := "Dependency-blocked Issues (1; graph preview)\n\"https://example.test/beads/work\" P1 \"Line\\n雪\"\n  blocked by \"https://example.test/beads/prereq\"\n"
	if err != nil || output != want || strings.Contains(output, "native-private-property") {
		t.Fatalf("human canonical output differs: %q %v", output, err)
	}
	if output, err := renderGraphIssueBlocked([]graphstore.BlockedIssue{item}, false, true); err != nil || output != "" {
		t.Fatalf("quiet emitted output: %q %v", output, err)
	}
	empty, err := renderGraphIssueBlocked(nil, true, false)
	if err != nil || !strings.Contains(empty, `"result":[]`) {
		t.Fatalf("empty result is not an array: %s %v", empty, err)
	}
	if output, err := renderGraphIssueBlocked(nil, false, false); err != nil || output != "Dependency-blocked Issues (0; graph preview)\n" {
		t.Fatalf("empty human: %q %v", output, err)
	}
	item.Issue.Properties.Description = strings.Repeat("x", graphIssueBlockedOutputLimit)
	if output, err := renderGraphIssueBlocked([]graphstore.BlockedIssue{item}, true, false); !errors.Is(err, graphstore.ErrLimitExceeded) || output != "" {
		t.Fatal("oversized output did not refuse completely")
	}
}

func TestGraphPreviewBlockedDispatchBeforeStore(t *testing.T) {
	savedActive, savedJSON, savedStructured := graphPreviewActive, jsonOutput, graphPreviewStructuredErrors
	graphPreviewActive, jsonOutput, graphPreviewStructuredErrors = true, false, false
	t.Cleanup(func() {
		graphPreviewActive, jsonOutput, graphPreviewStructuredErrors = savedActive, savedJSON, savedStructured
	})
	t.Setenv(maxRowsEnvVar, "")
	// Call the actual registered command with an unsupported flag. It must reach
	// graph admission and return before any legacy or graph store is opened.
	cmd := blockedPreviewCommand(t, "--parent=")
	err := cmd.RunE(cmd, nil)
	var exit *exitError
	if !errors.As(err, &exit) || exit.Code != 5 {
		t.Fatalf("registered blocked command bypassed graph admission: %v", err)
	}
}
