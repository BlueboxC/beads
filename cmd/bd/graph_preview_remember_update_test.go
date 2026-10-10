package main

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/graphstore"
)

func selectedRememberCommand(t *testing.T, flags []string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{}
	for _, name := range []string{"id", "title", "update", "if-revision", "body-file", "key", "properties"} {
		cmd.Flags().String(name, "", "")
	}
	cmd.Flags().Bool("stdin", false, "")
	cmd.Flags().Bool("create-only", false, "")
	if err := cmd.ParseFlags(flags); err != nil {
		t.Fatal(err)
	}
	return cmd
}

func TestGraphPreviewRememberSelectedPatchInput(t *testing.T) {
	for _, tc := range []struct {
		name                string
		flags, args         []string
		wantTitle, wantBody *string
		bad                 bool
	}{
		{"body-only", nil, []string{"  body 雪\r\n"}, nil, memoryPatchInputString("  body 雪\r\n"), false},
		{"clear-body", nil, []string{""}, nil, memoryPatchInputString(""), false},
		{"title-only", []string{"--title=  title 雪  "}, nil, memoryPatchInputString("  title 雪  "), nil, false},
		{"clear-title", []string{"--title="}, nil, memoryPatchInputString(""), nil, false},
		{"both", []string{"--title=new"}, []string{"body"}, memoryPatchInputString("new"), memoryPatchInputString("body"), false},
		{"both-empty", []string{"--title="}, []string{""}, memoryPatchInputString(""), memoryPatchInputString(""), false},
		{"properties-body", []string{`--properties={"body":"typed body"}`}, nil, nil, memoryPatchInputString("typed body"), false},
		{"properties-title", []string{`--properties={"title":"typed title"}`}, nil, memoryPatchInputString("typed title"), nil, false},
		{"properties-empty-noop", []string{`--properties={}`}, nil, nil, nil, false},
		{"properties-and-body", []string{`--properties={"title":"typed title"}`}, []string{"positional body"}, memoryPatchInputString("typed title"), memoryPatchInputString("positional body"), false},
		{"duplicate-body", []string{`--properties={"body":"body"}`}, []string{"body"}, nil, nil, true},
		{"duplicate-title", []string{"--title=title", `--properties={"title":"title"}`}, nil, nil, nil, true},
		{"unknown-property", []string{`--properties={"content":"body"}`}, nil, nil, nil, true},
		{"neither", nil, nil, nil, nil, true},
		{"invalid-title", []string{"--title=\xff"}, nil, nil, nil, true},
		{"false-stdin-with-title", []string{"--title=new", "--stdin=false"}, nil, nil, nil, true},
		{"multiple-bodies-with-title", []string{"--title=new"}, []string{"one", "two"}, nil, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := selectedRememberCommand(t, tc.flags)
			probe := &selectedRememberInputProbe{}
			cmd.SetIn(probe)
			title, body, err := graphPreviewRememberPatchInput(cmd, tc.args)
			if (err != nil) != tc.bad || !sameMemoryPatchInput(title, tc.wantTitle) || !sameMemoryPatchInput(body, tc.wantBody) || probe.reads != 0 {
				t.Fatalf("title=%v body=%v error=%v stdin reads=%d", title, body, err, probe.reads)
			}
		})
	}
}

func memoryPatchInputString(value string) *string { return &value }
func sameMemoryPatchInput(a, b *string) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

// Changed graph flags must reach workspace admission even with no positional
// body. Bare legacy calls retain their original one-argument requirement.
func TestGraphPreviewRememberSelectedArgumentAdmission(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []string
		graph bool
	}{
		{"selected", []string{"--update=beads/plan"}, true},
		{"create-only", []string{"--id=beads/plan", "--create-only"}, true},
		{"empty-selected", []string{"--update="}, true},
		{"guard-only", []string{"--if-revision=observed"}, true},
		{"empty-guard", []string{"--if-revision="}, true},
		{"existing-file", []string{"--body-file=body.md"}, true},
		{"properties", []string{`--properties={}`}, true},
		{"legacy-key", []string{"--key=plan"}, false},
		{"bare-legacy", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := selectedRememberCommand(t, tc.flags)
			if rememberGraphFlagsChanged(cmd) != tc.graph {
				t.Fatal("wrong graph admission selection")
			}
			if err := rememberArgs(cmd, nil); (err == nil) != tc.graph {
				t.Fatalf("wrong missing-body admission: %v", err)
			}
			if err := rememberArgs(cmd, []string{"content"}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type selectedRememberInputProbe struct{ reads int }

func (p *selectedRememberInputProbe) Read([]byte) (int, error) {
	p.reads++
	return 0, io.ErrUnexpectedEOF
}

func TestGraphPreviewRememberSelectedRefusesBeforeInput(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("GT_TOWN_ROOT", "")
	t.Setenv("GT_ROOT", "")
	oldConfig, oldReadonly := graphPreviewConfig, readonlyMode
	oldJSON, oldStructured := jsonOutput, graphPreviewStructuredErrors
	t.Cleanup(func() {
		graphPreviewConfig, readonlyMode = oldConfig, oldReadonly
		jsonOutput, graphPreviewStructuredErrors = oldJSON, oldStructured
	})
	graphPreviewConfig = &configfile.Config{GraphScopeURL: "https://example.invalid/"}
	jsonOutput, graphPreviewStructuredErrors = false, false
	for _, tc := range []struct {
		name     string
		flags    []string
		readonly bool
		code     int
	}{
		{"empty-guard", []string{"--update=beads/plan", "--if-revision="}, false, 2},
		{"invalid-guard", []string{"--update=beads/plan", "--if-revision=\xff"}, false, 2},
		{"oversized-guard", []string{"--update=beads/plan", "--if-revision=" + strings.Repeat("x", graphstore.PreviewVersionTokenLimit+1)}, false, 2},
		{"empty-selector", []string{"--update=", "--if-revision=observed"}, false, 2},
		{"foreign-selector", []string{"--update=https://foreign.invalid/beads/plan", "--if-revision=observed"}, false, 2},
		{"link-selector", []string{"--update=links/context", "--if-revision=observed"}, false, 2},
		{"create-and-update", []string{"--id=beads/new", "--update=beads/plan", "--if-revision=observed"}, false, 2},
		{"generated-with-guard", []string{"--title=New", "--if-revision=observed"}, false, 5},
		{"create-only-without-id", []string{"--create-only"}, false, 2},
		{"create-only-false", []string{"--id=beads/new", "--create-only=false"}, false, 2},
		{"create-only-with-guard", []string{"--id=beads/new", "--create-only", "--if-revision=observed"}, false, 2},
		{"create-only-with-update", []string{"--id=beads/new", "--create-only", "--update=beads/plan"}, false, 2},
		{"guard-without-selection", []string{"--if-revision=observed"}, false, 5},
		{"invalid-title", []string{"--update=beads/plan", "--if-revision=observed", "--title=\xff"}, false, 2},
		{"legacy-key", []string{"--update=beads/plan", "--if-revision=observed", "--key=legacy"}, false, 5},
		{"readonly", []string{"--update=beads/plan", "--if-revision=observed"}, true, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			readonlyMode = tc.readonly
			cmd := selectedRememberCommand(t, append(append([]string(nil), tc.flags...), "--stdin"))
			input := &selectedRememberInputProbe{}
			cmd.SetIn(input)
			err := runGraphPreviewRemember(cmd, nil)
			var failure *exitError
			if !errors.As(err, &failure) || failure.Code != tc.code || input.reads != 0 {
				t.Fatalf("refusal=%v reads=%d", err, input.reads)
			}
		})
	}
}
