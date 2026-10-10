package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/graphpatch"
	"github.com/steveyegge/beads/internal/storage/graphstore"
)

const memoryPropertiesPatchExample = `[{"op":"replace","path":"/body","value":"  雪\r\n  "}]`

// The current freeze policy includes both cwd and the selected graph directory.
func isolatePropertiesPatchAdmission(t *testing.T) {
	t.Helper()
	isolateBeadsDirForTest(t)
	t.Chdir(t.TempDir())
	t.Setenv("BD_MIGRATION_FREEZE_FILE", filepath.Join(t.TempDir(), "absent-freeze"))
	oldDir := graphPreviewDir
	graphPreviewDir = ""
	t.Cleanup(func() { graphPreviewDir = oldDir })
}

func memoryPropertiesPatchCommand(t *testing.T, flags ...string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{}
	for _, name := range []string{"patch", "properties", "if-revision", "if-source-revision", "title", "status", "notes", "actor", "body-file", "request-id", "metadata"} {
		cmd.Flags().String(name, "", "")
	}
	for _, name := range []string{"unconditional", "unconditional-source", "claim", "force", "readonly", "dry-run", "stdin"} {
		cmd.Flags().Bool(name, false, "")
	}
	if err := cmd.ParseFlags(flags); err != nil {
		t.Fatal(err)
	}
	return cmd
}

type memoryPropertiesPatchProbe struct {
	reader       io.Reader
	reads, bytes int
}

func (p *memoryPropertiesPatchProbe) Read(buf []byte) (int, error) {
	p.reads++
	if p.reader == nil {
		return 0, io.ErrUnexpectedEOF
	}
	n, err := p.reader.Read(buf)
	p.bytes += n
	return n, err
}

func TestGraphPreviewMemoryPropertiesPatchSources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "patch.json")
	if err := os.WriteFile(path, []byte(memoryPropertiesPatchExample), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, input string
		stdin       io.Reader
	}{
		{"literal", memoryPropertiesPatchExample, nil},
		{"file", "@" + path, nil},
		{"stdin", "@-", strings.NewReader(memoryPropertiesPatchExample)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probe := &memoryPropertiesPatchProbe{reader: tc.stdin}
			got, err := graphPreviewMemoryPropertiesPatchInput(tc.input, probe)
			if err != nil || !bytes.Equal(got, []byte(memoryPropertiesPatchExample)) {
				t.Fatalf("patch source changed bytes: %q %v", got, err)
			}
			if tc.name != "stdin" && probe.reads != 0 {
				t.Fatal("explicit literal/file read stdin")
			}
		})
	}
	for _, tc := range []struct{ name, input string }{
		{"empty-input", ""}, {"empty-operations", "[]"}, {"object", "{}"}, {"null", "null"},
		{"duplicate-operation-member", `[{"op":"replace","op":"add","path":"/body","value":"x"}]`},
		{"duplicate-value-member", `[{"op":"add","path":"/temp","value":{"x":1,"x":2}}]`},
		{"unpaired-surrogate", `[{"op":"replace","path":"/body","value":"\ud800"}]`},
		{"invalid-utf8", "[{\"op\":\"replace\",\"path\":\"/body\",\"value\":\"\xff\"}]"},
		{"unsupported-member", `[{"op":"replace","path":"/body","value":"x","unexpected":true}]`},
		{"missing-add-value", `[{"op":"add","path":"/body"}]`},
		{"unavailable-operation", `[{"op":"test","path":"/body","value":"x"}]`},
		{"invalid-pointer", `[{"op":"remove","path":"/bad~2escape"}]`},
		{"inadmissible-number", `[{"op":"add","path":"/temp","value":1e9999}]`},
		{"missing-file", "@" + filepath.Join(t.TempDir(), "missing.json")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := graphPreviewMemoryPropertiesPatchInput(tc.input, &memoryPropertiesPatchProbe{}); err == nil || got != nil {
				t.Fatalf("invalid input admitted: %q %v", got, err)
			}
		})
	}
	t.Run("read-error", func(t *testing.T) {
		if got, err := graphPreviewMemoryPropertiesPatchInput("@-", &memoryPropertiesPatchProbe{}); !errors.Is(err, io.ErrUnexpectedEOF) || got != nil {
			t.Fatalf("read error lost: %q %v", got, err)
		}
	})
}

func TestGraphPreviewMemoryPropertiesPatchInputBound(t *testing.T) {
	exact := memoryPropertiesPatchExample + strings.Repeat(" ", graphpatch.MaxInputBytes-len(memoryPropertiesPatchExample))
	if got, err := graphPreviewMemoryPropertiesPatchInput(exact, &memoryPropertiesPatchProbe{}); err != nil || len(got) != graphpatch.MaxInputBytes {
		t.Fatalf("exact input bound refused: len=%d err=%v", len(got), err)
	}
	probe := &memoryPropertiesPatchProbe{reader: strings.NewReader(exact + strings.Repeat(" ", 100))}
	if got, err := graphPreviewMemoryPropertiesPatchInput("@-", probe); err == nil || got != nil || probe.bytes != graphpatch.MaxInputBytes+1 {
		t.Fatalf("input not bounded before parse: bytes=%d err=%v", probe.bytes, err)
	}
	path := filepath.Join(t.TempDir(), "large.json")
	if err := os.WriteFile(path, []byte(exact+" "), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := graphPreviewMemoryPropertiesPatchInput("@"+path, &memoryPropertiesPatchProbe{}); err == nil || got != nil {
		t.Fatalf("oversized file admitted: %v", err)
	}
}

func TestGraphPreviewMemoryPropertiesPatchRequest(t *testing.T) {
	oldActor := actor
	t.Cleanup(func() { actor = oldActor })
	actor = "patch-author"
	for _, flags := range [][]string{{"--if-revision=observed"}, {}} {
		cmd := memoryPropertiesPatchCommand(t, append([]string{"--patch=" + memoryPropertiesPatchExample}, flags...)...)
		request, err := graphPreviewMemoryPropertiesPatchRequest(cmd, "beads/plan")
		if err != nil || request.Path != "beads/plan" || request.Actor != "patch-author" || !bytes.Equal(request.Patch, []byte(memoryPropertiesPatchExample)) || request.Unconditional != (len(flags) == 0) || (!request.Unconditional && request.ExpectedRevision != "observed") {
			t.Fatalf("patch request lost intent: %+v %v", request, err)
		}
	}
	// A nonempty ordered patch may restore its predecessor. The CLI validates
	// syntax only; no-op equality belongs to the checked storage transaction.
	reversing := `[{"op":"add","path":"/temporary","value":[]},{"op":"remove","path":"/temporary"}]`
	if raw, err := graphPreviewMemoryPropertiesPatchInput(reversing, &memoryPropertiesPatchProbe{}); err != nil || string(raw) != reversing {
		t.Fatalf("reversing syntax refused: %v", err)
	}
}

func TestGraphPreviewMemoryPropertiesPatchRefusesBeforeInput(t *testing.T) {
	isolatePropertiesPatchAdmission(t)
	oldConfig, oldReadonly := graphPreviewConfig, readonlyMode
	oldJSON, oldStructured := jsonOutput, graphPreviewStructuredErrors
	t.Cleanup(func() {
		graphPreviewConfig, readonlyMode = oldConfig, oldReadonly
		jsonOutput, graphPreviewStructuredErrors = oldJSON, oldStructured
	})
	graphPreviewConfig = &configfile.Config{GraphScopeURL: "https://example.invalid/"}
	jsonOutput, graphPreviewStructuredErrors = false, false
	for _, tc := range []struct {
		name        string
		args, flags []string
		readonly    bool
		code        int
	}{
		{"missing-selector", nil, []string{}, false, 2},
		{"multiple-selectors", []string{"beads/a", "beads/b"}, []string{}, false, 2},
		{"foreign-selector", []string{"https://foreign.invalid/beads/a"}, []string{}, false, 2},
		{"unsupported-selector", []string{"alias/old-id"}, []string{}, false, 2},
		{"link-selector", []string{"links/context"}, []string{"--properties={}"}, false, 5},
		{"empty-guard", []string{"beads/plan"}, []string{"--if-revision="}, false, 2},
		{"invalid-guard", []string{"beads/plan"}, []string{"--if-revision=\xff"}, false, 2},
		{"oversized-guard", []string{"beads/plan"}, []string{"--if-revision=" + strings.Repeat("x", graphstore.PreviewVersionTokenLimit+1)}, false, 2},
		{"replacement-conflict", []string{"beads/plan"}, []string{"--properties={}"}, false, 5},
		{"Issue-title-conflict", []string{"beads/work"}, []string{"--title=Issue"}, false, 5},
		{"claim-conflict", []string{"beads/work"}, []string{"--claim=true"}, false, 5},
		{"false-claim-conflict", []string{"beads/work"}, []string{"--claim=false"}, false, 5},
		{"source-guard", []string{"beads/plan"}, []string{"--if-source-revision=old"}, false, 5},
		{"false-source-unconditional", []string{"beads/plan"}, []string{"--unconditional-source=false"}, false, 5},
		{"notes-conflict", []string{"beads/plan"}, []string{"--notes="}, false, 5},
		{"request-status-unavailable", []string{"beads/plan"}, []string{"--request-id=id"}, false, 5},
		{"readonly", []string{"beads/plan"}, []string{}, true, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			readonlyMode = tc.readonly
			cmd := memoryPropertiesPatchCommand(t, append([]string{"--patch=@-"}, tc.flags...)...)
			probe := &memoryPropertiesPatchProbe{}
			cmd.SetIn(probe)
			err := runGraphPreviewUpdate(cmd, tc.args)
			var failure *exitError
			if !errors.As(err, &failure) || failure.Code != tc.code || probe.reads != 0 {
				t.Fatalf("refusal consumed input or wrong code: %v reads=%d", err, probe.reads)
			}
		})
	}
	readonlyMode = false
	t.Run("migration-freeze", func(t *testing.T) {
		freeze := filepath.Join(t.TempDir(), "MIGRATION-FREEZE")
		if err := os.WriteFile(freeze, []byte("patch-test\t2026-09-28T00:00:00Z\tfreeze\n"), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BD_MIGRATION_FREEZE_FILE", freeze)
		cmd := memoryPropertiesPatchCommand(t, "--patch=@-")
		probe := &memoryPropertiesPatchProbe{}
		cmd.SetIn(probe)
		err := runGraphPreviewUpdate(cmd, []string{"beads/plan"})
		var failure *exitError
		if !errors.As(err, &failure) || failure.Code != 5 || probe.reads != 0 {
			t.Fatalf("freeze bypassed before input: %v reads=%d", err, probe.reads)
		}
	})
}

func TestGraphPreviewMemoryPropertiesPatchLegacyAdmission(t *testing.T) {
	isolatePropertiesPatchAdmission(t)
	for _, name := range []string{"BEADS_DB", "BD_DB", "BD_GRAPH_MODE"} {
		t.Setenv(name, "")
	}
	t.Setenv("BEADS_DIR", t.TempDir())
	oldUpdate, oldActive, oldConfig, oldDir := updateCmd, graphPreviewActive, graphPreviewConfig, graphPreviewDir
	oldJSON, oldStructured := jsonOutput, graphPreviewStructuredErrors
	t.Cleanup(func() {
		updateCmd, graphPreviewActive, graphPreviewConfig, graphPreviewDir = oldUpdate, oldActive, oldConfig, oldDir
		jsonOutput, graphPreviewStructuredErrors = oldJSON, oldStructured
	})
	jsonOutput, graphPreviewStructuredErrors = false, false
	cmd := memoryPropertiesPatchCommand(t, "--patch=@-")
	updateCmd = cmd
	probe := &memoryPropertiesPatchProbe{}
	cmd.SetIn(probe)
	handled, err := admitGraphPreview(cmd)
	var failure *exitError
	if !handled || !errors.As(err, &failure) || failure.Code != 5 || probe.reads != 0 || graphPreviewActive || !reflect.DeepEqual(graphPreviewConfig, (*configfile.Config)(nil)) {
		t.Fatalf("legacy route admitted graph patch: handled=%t err=%v reads=%d", handled, err, probe.reads)
	}
}

func TestGraphPreviewMemoryPropertiesPatchSyntaxBeforeStore(t *testing.T) {
	isolatePropertiesPatchAdmission(t)
	oldConfig, oldReadonly := graphPreviewConfig, readonlyMode
	oldJSON, oldStructured := jsonOutput, graphPreviewStructuredErrors
	t.Cleanup(func() {
		graphPreviewConfig, readonlyMode = oldConfig, oldReadonly
		jsonOutput, graphPreviewStructuredErrors = oldJSON, oldStructured
	})
	// There is deliberately no initialized workspace/database to open. Invalid
	// patch syntax must fail at its own boundary before any backend admission.
	graphPreviewConfig = &configfile.Config{GraphScopeURL: "https://example.invalid/"}
	readonlyMode, jsonOutput, graphPreviewStructuredErrors = false, false, false
	for _, tc := range []struct{ name, input string }{
		{"empty-list", "[]"},
		{"invalid-operation", `[{"op":"move","from":"/body","path":"/title"}]`},
		{"missing-file", "@" + filepath.Join(t.TempDir(), "missing.json")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := memoryPropertiesPatchCommand(t, "--patch="+tc.input)
			probe := &memoryPropertiesPatchProbe{}
			cmd.SetIn(probe)
			err := runGraphPreviewUpdate(cmd, []string{"beads/plan"})
			var failure *exitError
			if !errors.As(err, &failure) || failure.Code != 2 || probe.reads != 0 {
				t.Fatalf("invalid patch reached backend or stdin: %v reads=%d", err, probe.reads)
			}
		})
	}
}
